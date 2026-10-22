// Package transport 承载 SIWC 出站传输的核心改写规则与 HTTP 转发。
package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"siwc-transport-plugin/internal/pluginconfig"
)

// chatGPTCodexPathPrefix 是宿主为 OpenAI OAuth 账号构造的 codex 出口前缀。
const chatGPTCodexPathPrefix = "/backend-api/codex"

// RewriteURL 把宿主构造的上游 URL 改写为 SIWC 直连路由。
//
// 映射规则：chatgpt.com/backend-api/codex/<rest> → <directBase>/v1/<rest>。
// 例如 /backend-api/codex/responses → /v1/responses，
//
//	/backend-api/codex/responses/input_tokens → /v1/responses/input_tokens。
//
// 其余 URL（api.openai.com、自定义 base 等）原样保留。
func RewriteURL(rawURL string, cfg pluginconfig.Config) (string, bool, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false, err
	}
	if !cfg.RewriteChatGPTCodexURLs {
		return rawURL, false, nil
	}
	if !strings.EqualFold(parsed.Hostname(), "chatgpt.com") {
		return rawURL, false, nil
	}
	if !strings.HasPrefix(parsed.Path, chatGPTCodexPathPrefix+"/") {
		// chatgpt.com 下的其他 backend-api 路径不属于 codex 出口，不改写。
		return rawURL, false, nil
	}
	rest := strings.TrimPrefix(parsed.Path, chatGPTCodexPathPrefix)
	rest = "/" + strings.TrimLeft(rest, "/")

	base, err := url.Parse(cfg.DirectBaseURL)
	if err != nil {
		return "", false, err
	}
	rewritten := *base
	if strings.HasSuffix(rewritten.Path, "/v1") {
		// base 已带 /v1（默认 base 无路径；显式配置了带版本 base 时避免重复）。
		rewritten.Path = strings.TrimRight(rewritten.Path, "/") + rest
	} else {
		rewritten.Path = strings.TrimRight(rewritten.Path, "/") + "/v1" + rest
	}
	rewritten.RawQuery = parsed.RawQuery
	return rewritten.String(), true, nil
}

// siwcHeaderAllowlist lists the request headers (lowercase) copied from the
// host request. Codex identity headers (chatgpt-account-id, originator,
// conversation_id, x-codex-*, OpenAI-Beta, ...) are dropped. session_id is not
// copied either: Forwarder.Decide rebuilds it from the conversation's
// prompt_cache_key, because the direct route uses session_id for prompt-cache
// affinity and dropping it scatters every conversation across cache-cold
// machines.
var siwcHeaderAllowlist = map[string]bool{
	"content-type": true,
	"accept":       true,
}

// BuildSIWCHeaders builds the minimal outbound header set for a SIWC account.
// Authorization carries the SIWC access token resolved (and refreshed when
// needed) by the host.
func BuildSIWCHeaders(source http.Header, token string, cfg pluginconfig.Config) http.Header {
	headers := http.Header{}
	contentType := source.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	headers.Set("Content-Type", contentType)
	if accept := source.Get("Accept"); accept != "" {
		headers.Set("Accept", accept)
	}
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("User-Agent", cfg.UserAgent)
	return headers
}

// PreservePassthroughHeaders 非_SIWC 账号透明转发时剥离逐跳头。
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func StripHopByHopHeaders(headers http.Header) {
	for _, name := range hopByHopHeaders {
		headers.Del(name)
	}
	if values := headers.Values("Connection"); len(values) > 0 {
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				headers.Del(strings.TrimSpace(token))
			}
		}
	}
}

// ApplySIWCBodyRules 对 /responses 请求体施加 SIWC 强制规则：
// store=false 且 stream=true（文档要求每次推理必须如此）。
// 请求体不是 JSON 对象时原样返回（由上游报错）。
func ApplySIWCBodyRules(body []byte, cfg pluginconfig.Config, isResponsesRequest bool) ([]byte, bool) {
	if !isResponsesRequest || len(bytes.TrimSpace(body)) == 0 {
		return body, false
	}
	if !gjson.ValidBytes(body) {
		return body, false
	}
	out := body
	changed := false
	// 剥掉 client_metadata：这是 Codex 专属字段，直连路由不需要；且指纹收敛
	// 往里面写 turn_id（每次新 UUID）和 turn_started_at_unix_ms（每次新时间戳），
	// 如果前缀缓存哈希包含此字段，会导致同一会话的每轮请求前缀都不匹配。
	if gjson.GetBytes(out, "client_metadata").Exists() {
		next, err := sjson.DeleteBytes(out, "client_metadata")
		if err == nil {
			out = next
			changed = true
		}
	}
	// 确定性序列化 input：客户端（Rust/TS HashMap）可能对同一消息产生不同
	// JSON 键顺序，导致字节级前缀每轮都变 → SIWC 前缀缓存永远 miss。
	// 解码→重编码（Go encoding/json 按键名排序）确保同一语义永远同一字节。
	if normalized, normChanged := NormalizeInputSerialization(out); normChanged {
		out = normalized
		changed = true
	}
	// 确定性排序 tools：同一集合的工具无论客户端以什么顺序发送，排序后
	// 的字节序列完全一致。这消除了因顺序变化导致的前缀缓存失效——
	// tools 是前缀哈希的一部分，顺序变了整个缓存从头作废。
	// 注意：工具的增删仍会导致前缀变化（客户端行为，无法网关侧解决）。
	if sorted, sortedChanged := SortToolsDeterministic(out); sortedChanged {
		out = sorted
		changed = true
	}
	if cfg.ForceStoreFalse {
		if store := gjson.GetBytes(out, "store"); !store.Exists() || store.Bool() {
			next, err := sjson.SetBytes(out, "store", false)
			if err == nil {
				out = next
				changed = true
			}
		}
	}
	if cfg.ForceStream {
		if stream := gjson.GetBytes(out, "stream"); !stream.Exists() || !stream.Bool() {
			next, err := sjson.SetBytes(out, "stream", true)
			if err == nil {
				out = next
				changed = true
			}
		}
	}
	return out, changed
}

// siwcUnsupportedItemTypes 是实测确认 SIWC 直连路由（subscription sharing
// 授权白名单）不接受且无法有意义转换的 input 项类型：
//   - computer_call / computer_call_output：computer use 会话历史（含截图，
//     无法转换为文本消息，只能剥除）。
//
// agent_message 不在剥除清单——它会被 ConvertSIWCAgentMessages 转换为
// message(role=assistant)，保留内容且维持前缀一致性（缓存友好）。
var siwcUnsupportedItemTypes = map[string]bool{
	"computer_call":        true,
	"computer_call_output": true,
}

// ConvertSIWCAgentMessages 把 Codex 多代理子代理输出项 agent_message 转换为
// 直连路由接受的 message(role=assistant)。转换是确定性的（同一输入永远产生
// 同一输出），因此转换后的前缀在多轮请求间保持一致，缓存可以命中——这与
// 剥除（改变项数导致前缀永远不稳定）有本质区别。
// NormalizeInputSerialization 对 input 数组做确定性重编码：解码每个 item
// 为 map[string]any 再用 encoding/json 重序列化（Go 自动按键名排序）。
// 这消除客户端（Rust/TS HashMap 无序迭代）在不同请求中对同一消息产生
// 不同 JSON 键顺序的问题——SIWC 前缀缓存按字节匹配，键顺序变了就 miss。
// 性能：只处理 input 数组（跳过 instructions/tools 等顶层字段），单次
// 解码+编码 O(n)，对 300K token 的长会话也在毫秒级。
func NormalizeInputSerialization(body []byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	items := input.Array()
	normalized := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		var obj map[string]any
		if err := json.Unmarshal([]byte(item.Raw), &obj); err != nil {
			normalized = append(normalized, item.Raw)
			continue
		}
		reencoded, err := json.Marshal(obj)
		if err != nil {
			normalized = append(normalized, item.Raw)
			continue
		}
		if string(reencoded) != item.Raw {
			changed = true
		}
		normalized = append(normalized, string(reencoded))
	}
	if !changed {
		return body, false
	}
	rebuilt := "[" + strings.Join(normalized, ",") + "]"
	out, err := sjson.SetRawBytes(body, "input", []byte(rebuilt))
	if err != nil {
		return body, false
	}
	return out, true
}

// SortToolsDeterministic 对顶层 tools 数组按 type:name 排序，确保同一集合
// 的工具无论客户端以什么顺序发送，出站字节序列完全一致。
func SortToolsDeterministic(body []byte) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, false
	}
	items := tools.Array()
	if len(items) < 2 {
		return body, false
	}
	// 检查是否已排序
	alreadySorted := true
	prevKey := ""
	for _, item := range items {
		key := item.Get("type").String() + ":" + item.Get("name").String()
		if prevKey != "" && key < prevKey {
			alreadySorted = false
			break
		}
		prevKey = key
	}
	if alreadySorted {
		return body, false
	}
	// 排序
	type toolEntry struct {
		key string
		raw string
	}
	entries := make([]toolEntry, 0, len(items))
	for _, item := range items {
		key := item.Get("type").String() + ":" + item.Get("name").String()
		entries = append(entries, toolEntry{key, item.Raw})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	sorted := make([]string, 0, len(entries))
	for _, e := range entries {
		sorted = append(sorted, e.raw)
	}
	out, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(sorted, ",")+"]"))
	if err != nil {
		return body, false
	}
	return out, true
}

func ConvertSIWCAgentMessages(body []byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	// 快速路径：无 agent_message 则不改写。
	hasAgent := false
	for _, item := range input.Array() {
		if item.Get("type").String() == "agent_message" {
			hasAgent = true
			break
		}
	}
	if !hasAgent {
		return body, false
	}

	items := input.Array()
	converted := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if item.Get("type").String() != "agent_message" {
			converted = append(converted, item.Raw)
			continue
		}
		// agent_message → message(role=assistant)：丢 author；content 只保留
		// output_text/refusal part（白名单），input_text 改为 output_text，
		// encrypted_content 等其他类型全丢（直连路由 400 实测确认）。
		contentParts := item.Get("content").Array()
		keptParts := make([]string, 0, len(contentParts))
		for _, part := range contentParts {
			partType := part.Get("type").String()
			switch partType {
			case "output_text", "refusal":
				keptParts = append(keptParts, part.Raw)
			case "input_text":
				// 改类型为 output_text，保留 text 内容
				text := part.Get("text").String()
				keptParts = append(keptParts, `{"type":"output_text","text":`+strconv.Quote(text)+`}`)
			default:
				// encrypted_content / input_image / input_file 等全丢
			}
		}
		if len(keptParts) == 0 {
			keptParts = append(keptParts, `{"type":"output_text","text":"(agent message content unavailable)"}`)
		}
		rebuilt := `{"type":"message","role":"assistant","content":[` + strings.Join(keptParts, ",") + `]}`
		converted = append(converted, rebuilt)
		changed = true
	}
	if !changed {
		return body, false
	}
	out, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(converted, ",")+"]"))
	if err != nil {
		return body, false
	}
	return out, true
}

// StripUnsupportedSIWCItems 从请求体 input 数组中剥除 SIWC 不支持的工具项。
// input 为字符串或缺失时原样返回。返回改写后的 body 与是否发生变更。
func StripUnsupportedSIWCItems(body []byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	items := input.Array()
	kept := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if siwcUnsupportedItemTypes[item.Get("type").String()] {
			changed = true
			continue
		}
		kept = append(kept, item.Raw)
	}
	if !changed {
		return body, false
	}
	rebuilt := "[" + strings.Join(kept, ",") + "]"
	out, err := sjson.SetRawBytes(body, "input", []byte(rebuilt))
	if err != nil {
		return body, false
	}
	return out, true
}

// siwcUnsupportedToolTypes are top-level tool types the SIWC route rejects with
// subscription_sharing_unsupported_capability (param=tools). Client-side
// execution does not make tool_search acceptable on this route.
var siwcUnsupportedToolTypes = map[string]bool{
	"tool_search": true,
}

// StripUnsupportedSIWCTools removes tool types the SIWC route rejects. Without
// tool_search, deferred tools can never be loaded on demand, so their
// defer_loading flags are dropped to expose them directly.
func StripUnsupportedSIWCTools(body []byte) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, false
	}
	items := tools.Array()
	kept := make([]string, 0, len(items))
	stripped := false
	for _, item := range items {
		if siwcUnsupportedToolTypes[strings.TrimSpace(item.Get("type").String())] {
			stripped = true
			continue
		}
		kept = append(kept, item.Raw)
	}
	if !stripped {
		return body, false
	}
	for index, raw := range kept {
		kept[index] = dropDeferLoading(raw)
	}
	out, err := sjson.SetRawBytes(body, "tools", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return body, false
	}
	return out, true
}

// dropDeferLoading removes defer_loading from a tool and from the tools nested
// in a namespace tool.
func dropDeferLoading(raw string) string {
	out := raw
	if gjson.Get(out, "defer_loading").Exists() {
		if next, err := sjson.Delete(out, "defer_loading"); err == nil {
			out = next
		}
	}
	nested := gjson.Get(out, "tools")
	if !nested.IsArray() {
		return out
	}
	children := nested.Array()
	rebuilt := make([]string, 0, len(children))
	changed := false
	for _, child := range children {
		childRaw := child.Raw
		if child.Get("defer_loading").Exists() {
			if next, err := sjson.Delete(childRaw, "defer_loading"); err == nil {
				childRaw = next
				changed = true
			}
		}
		rebuilt = append(rebuilt, childRaw)
	}
	if !changed {
		return out
	}
	if next, err := sjson.SetRaw(out, "tools", "["+strings.Join(rebuilt, ",")+"]"); err == nil {
		return next
	}
	return out
}

// siwcModelOutputItemTypes are input item types produced by the model. The
// items before the first of them form the conversation's opening turn, which
// stays identical for every later request of the same conversation.
var siwcModelOutputItemTypes = map[string]bool{
	"reasoning":             true,
	"function_call":         true,
	"custom_tool_call":      true,
	"local_shell_call":      true,
	"web_search_call":       true,
	"tool_search_call":      true,
	"image_generation_call": true,
	"computer_call":         true,
	"mcp_call":              true,
	"code_interpreter_call": true,
	"file_search_call":      true,
	"agent_message":         true,
}

// EnsureSIWCPromptCacheKey keeps a client-provided prompt_cache_key and
// otherwise derives one that is stable for the whole conversation. Upstream
// routes cache lookups by prompt prefix plus prompt_cache_key: without a key,
// every Codex conversation of a model shares the same instructions prefix,
// overflows one routing bucket and keeps landing on cache-cold machines.
func EnsureSIWCPromptCacheKey(body []byte) (out []byte, key string, synthesized bool) {
	if existing := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); existing != "" {
		return body, existing, false
	}
	seed := siwcConversationSeed(body)
	if seed == nil {
		return body, "", false
	}
	key = formatSeedAsUUID(seed)
	next, err := sjson.SetBytes(body, "prompt_cache_key", key)
	if err != nil {
		return body, "", false
	}
	return next, key, true
}

// siwcConversationSeed hashes the instructions and the opening turn of the
// conversation. It returns nil when the request has no user input to anchor
// on, so input-less requests never share one tenant-wide key.
func siwcConversationSeed(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	hash := sha256.New()
	hash.Write([]byte("siwc-conversation:v1\n"))
	hash.Write([]byte(gjson.GetBytes(body, "instructions").String()))
	hash.Write([]byte{0})
	switch {
	case input.Type == gjson.String:
		if strings.TrimSpace(input.String()) == "" {
			return nil
		}
		hash.Write([]byte(input.String()))
	case input.IsArray():
		opening := 0
		for _, item := range input.Array() {
			if isSIWCModelOutputItem(item) {
				break
			}
			hash.Write([]byte(item.Raw))
			hash.Write([]byte{0})
			opening++
		}
		if opening == 0 {
			return nil
		}
	default:
		return nil
	}
	return hash.Sum(nil)
}

func isSIWCModelOutputItem(item gjson.Result) bool {
	if siwcModelOutputItemTypes[strings.TrimSpace(item.Get("type").String())] {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "assistant")
}

// formatSeedAsUUID renders a digest in the UUID shape Codex itself uses for
// prompt_cache_key.
func formatSeedAsUUID(seed []byte) string {
	encoded := hex.EncodeToString(seed[:16])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// IsResponsesRequest 判断（可能已改写的）目标 URL 是否为 Responses 推理端点。
func IsResponsesRequest(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	path := strings.TrimRight(parsed.Path, "/")
	return strings.HasSuffix(path, "/responses")
}
