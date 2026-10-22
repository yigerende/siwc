package transport

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"

	"siwc-transport-plugin/internal/pluginconfig"
)

func testConfig() pluginconfig.Config { return pluginconfig.Default() }

func TestRewriteURL_CodexResponsesToDirectRoute(t *testing.T) {
	rewritten, changed, err := RewriteURL("https://chatgpt.com/backend-api/codex/responses", testConfig())
	if err != nil || !changed {
		t.Fatalf("rewrite failed: changed=%v err=%v", changed, err)
	}
	if rewritten != "https://api.openai.com/v1/responses" {
		t.Fatalf("rewritten = %q", rewritten)
	}
}

func TestRewriteURL_CodexInputTokensToDirectRoute(t *testing.T) {
	rewritten, _, err := RewriteURL("https://chatgpt.com/backend-api/codex/responses/input_tokens", testConfig())
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	if rewritten != "https://api.openai.com/v1/responses/input_tokens" {
		t.Fatalf("rewritten = %q", rewritten)
	}
}

func TestRewriteURL_NonCodexURLsUntouched(t *testing.T) {
	for _, rawURL := range []string{
		"https://api.openai.com/v1/responses",
		"https://api.openai.com/v1/models",
		"https://chatgpt.com/backend-api/sentinel/chat-requirements",
		"https://custom.example.com/v1/responses",
	} {
		rewritten, changed, err := RewriteURL(rawURL, testConfig())
		if err != nil || changed || rewritten != rawURL {
			t.Fatalf("url %q should pass through: got %q changed=%v err=%v", rawURL, rewritten, changed, err)
		}
	}
}

func TestRewriteURL_CanBeDisabled(t *testing.T) {
	config := testConfig()
	config.RewriteChatGPTCodexURLs = false
	rewritten, changed, err := RewriteURL("https://chatgpt.com/backend-api/codex/responses", config)
	if err != nil || changed || rewritten != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("disabled rewrite must be a no-op: %q %v %v", rewritten, changed, err)
	}
}

func TestBuildSIWCHeaders_MinimalSet(t *testing.T) {
	source := http.Header{}
	source.Set("Content-Type", "application/json")
	source.Set("Accept", "text/event-stream")
	source.Set("Authorization", "Bearer original-codex-token")
	source.Set("chatgpt-account-id", "acct_1")
	source.Set("originator", "codex_cli_rs")
	source.Set("session_id", "sess_1")
	source.Set("conversation_id", "conv_1")
	source.Set("OpenAI-Beta", "responses=experimental")
	source.Set("x-codex-turn-state", "opaque")

	headers := BuildSIWCHeaders(source, "siwc-access-token", testConfig())
	if got := headers.Get("Authorization"); got != "Bearer siwc-access-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := headers.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
	if headers.Get("User-Agent") == "" {
		t.Fatal("User-Agent must be set")
	}
	for _, banned := range []string{"chatgpt-account-id", "originator", "session_id", "conversation_id", "OpenAI-Beta", "x-codex-turn-state"} {
		if headers.Get(banned) != "" {
			t.Fatalf("header %q must not be forwarded", banned)
		}
	}
}

func TestApplySIWCBodyRules_ForcesStoreFalseAndStreamTrue(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","input":[{"role":"user","content":"hi"}],"store":true}`)
	out, changed := ApplySIWCBodyRules(body, testConfig(), true)
	if !changed {
		t.Fatal("body must be rewritten")
	}
	// NormalizeInputSerialization 会按键名排序：content < role
	expected := `{"model":"gpt-6.1-sol","input":[{"content":"hi","role":"user"}],"store":false,"stream":true}`
	if string(out) != expected {
		t.Fatalf("out = %s, want = %s", out, expected)
	}
}

func TestApplySIWCBodyRules_KeepsAlreadyConformantBody(t *testing.T) {
	body := []byte(`{"model":"m","store":false,"stream":true}`)
	out, changed := ApplySIWCBodyRules(body, testConfig(), true)
	if changed || string(out) != string(body) {
		t.Fatalf("conformant body should be untouched: %s", out)
	}
}

func TestApplySIWCBodyRules_NonResponsesUntouched(t *testing.T) {
	body := []byte(`{"store":true}`)
	out, changed := ApplySIWCBodyRules(body, testConfig(), false)
	if changed || string(out) != string(body) {
		t.Fatal("non-responses request body must be untouched")
	}
}

func TestIsResponsesRequest(t *testing.T) {
	if !IsResponsesRequest("https://api.openai.com/v1/responses") {
		t.Fatal("/v1/responses should match")
	}
	if !IsResponsesRequest("https://api.openai.com/v1/responses?x=1") {
		t.Fatal("/v1/responses with query should match")
	}
	if IsResponsesRequest("https://api.openai.com/v1/responses/input_tokens") {
		t.Fatal("input_tokens must not be treated as an inference request")
	}
	if IsResponsesRequest("https://api.openai.com/v1/models") {
		t.Fatal("/v1/models must not match")
	}
}

func TestStripUnsupportedSIWCItems(t *testing.T) {
	body := []byte(`{"model":"m","input":[` +
		`{"type":"computer_call","call_id":"c1","action":{"type":"screenshot"}},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"type":"computer_call_output","call_id":"c1","output":"img"}],` +
		`"store":false,"stream":true}`)
	out, changed := StripUnsupportedSIWCItems(body)
	if !changed {
		t.Fatal("computer_call 项必须被剥除")
	}
	want := `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"store":false,"stream":true}`
	if got := string(out); got != want {
		t.Fatalf("out = %s", got)
	}

	clean := []byte(`{"input":[{"type":"message","role":"user","content":[]}],"store":false}`)
	if _, changed := StripUnsupportedSIWCItems(clean); changed {
		t.Fatal("干净 body 不应被改写")
	}

	textOnly := []byte(`{"input":"hello"}`)
	if out, changed := StripUnsupportedSIWCItems(textOnly); changed || string(out) != string(textOnly) {
		t.Fatal("字符串 input 应原样返回")
	}
}

func TestConvertSIWCAgentMessages(t *testing.T) {
	body := []byte(`{"model":"m","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"type":"agent_message","author":"sub-agent-1","content":[{"type":"output_text","text":"I found 3 files"}]},` +
		`{"type":"agent_message","author":"sub-agent-2","content":[{"type":"output_text","text":"Analysis complete"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"thanks"}]}],` +
		`"store":false,"stream":true}`)
	out, changed := ConvertSIWCAgentMessages(body)
	if !changed {
		t.Fatal("agent_message must be converted")
	}
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	if items[1].Get("type").String() != "message" || items[1].Get("role").String() != "assistant" {
		t.Fatalf("item[1] should be message/assistant, got %s/%s", items[1].Get("type").String(), items[1].Get("role").String())
	}
	if items[1].Get("content.0.text").String() != "I found 3 files" {
		t.Fatal("content must be preserved")
	}
	out2, _ := ConvertSIWCAgentMessages(body)
	if string(out) != string(out2) {
		t.Fatal("conversion must be deterministic for cache prefix stability")
	}
	clean := []byte(`{"input":[{"type":"message","role":"user","content":[]}],"store":false}`)
	if _, changed := ConvertSIWCAgentMessages(clean); changed {
		t.Fatal("no agent_message should not trigger conversion")
	}
}

func TestStripUnsupportedSIWCTools_RemovesToolSearchAndDeferLoading(t *testing.T) {
	body := []byte(`{"model":"m","tools":[` +
		`{"type":"function","name":"exec_command","parameters":{}},` +
		`{"type":"namespace","name":"mcp__app","tools":[{"type":"function","name":"read","defer_loading":true,"parameters":{}}]},` +
		`{"type":"function","name":"deferred","defer_loading":true,"parameters":{}},` +
		`{"type":"tool_search"},` +
		`{"type":"web_search"}],"input":[]}`)
	out, changed := StripUnsupportedSIWCTools(body)
	if !changed {
		t.Fatal("tool_search must be stripped")
	}
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools, got %d: %s", len(tools), out)
	}
	for _, tool := range tools {
		if tool.Get("type").String() == "tool_search" {
			t.Fatalf("tool_search survived: %s", out)
		}
	}
	if gjson.GetBytes(out, "tools.#.defer_loading").String() != "[]" {
		t.Fatalf("top-level defer_loading must be dropped: %s", out)
	}
	if gjson.GetBytes(out, "tools.1.tools.0.defer_loading").Exists() {
		t.Fatalf("namespace child defer_loading must be dropped: %s", out)
	}
	if gjson.GetBytes(out, "tools.1.tools.0.name").String() != "read" {
		t.Fatalf("namespace child must be kept: %s", out)
	}
}

func TestStripUnsupportedSIWCTools_LeavesBodyWithoutToolSearch(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"f","defer_loading":true}],"input":[]}`)
	out, changed := StripUnsupportedSIWCTools(body)
	if changed || string(out) != string(body) {
		t.Fatalf("body without tool_search must be untouched: %s", out)
	}
}

const codexOpeningItems = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions"}]},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>"}]},`

func codexTurnBody(firstPrompt string, followUp string) []byte {
	input := codexOpeningItems +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + firstPrompt + `"}]}`
	if followUp != "" {
		input += `,{"type":"reasoning","summary":[],"encrypted_content":"enc"},` +
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},` +
			`{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"c1","output":"ok"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + followUp + `"}]}`
	}
	return []byte(`{"model":"gpt-6-astra","instructions":"You are Codex.","input":[` + input + `],"store":false,"stream":true}`)
}

func TestEnsureSIWCPromptCacheKey_StableAcrossTurns(t *testing.T) {
	_, firstKey, synthesized := EnsureSIWCPromptCacheKey(codexTurnBody("fix the bug", ""))
	if !synthesized || firstKey == "" {
		t.Fatal("a key must be synthesized when the client sent none")
	}
	out, laterKey, _ := EnsureSIWCPromptCacheKey(codexTurnBody("fix the bug", "now add tests"))
	if laterKey != firstKey {
		t.Fatalf("key must stay stable across turns: %q vs %q", firstKey, laterKey)
	}
	if got := gjson.GetBytes(out, "prompt_cache_key").String(); got != laterKey {
		t.Fatalf("prompt_cache_key = %q, want %q", got, laterKey)
	}
	if len(firstKey) != 36 {
		t.Fatalf("key should use the UUID shape, got %q", firstKey)
	}
}

func TestEnsureSIWCPromptCacheKey_DistinctConversations(t *testing.T) {
	_, keyA, _ := EnsureSIWCPromptCacheKey(codexTurnBody("fix the bug", "continue"))
	_, keyB, _ := EnsureSIWCPromptCacheKey(codexTurnBody("write the docs", "continue"))
	if keyA == keyB {
		t.Fatal("conversations sharing only the project preamble must not share a key")
	}
}

func TestEnsureSIWCPromptCacheKey_KeepsClientKey(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"019a-client","input":[{"type":"message","role":"user","content":"hi"}]}`)
	out, key, synthesized := EnsureSIWCPromptCacheKey(body)
	if synthesized || key != "019a-client" || string(out) != string(body) {
		t.Fatalf("client key must be kept untouched: key=%q synthesized=%v out=%s", key, synthesized, out)
	}
}

func TestEnsureSIWCPromptCacheKey_NoAnchor(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"instructions":"x","input":[]}`),
		[]byte(`{"instructions":"x"}`),
		[]byte(`{"input":[{"type":"reasoning","summary":[]}]}`),
	} {
		out, key, synthesized := EnsureSIWCPromptCacheKey(body)
		if synthesized || key != "" || string(out) != string(body) {
			t.Fatalf("no user anchor must leave the body untouched: %s", body)
		}
	}
}
