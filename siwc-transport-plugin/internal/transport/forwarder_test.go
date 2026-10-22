package transport

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"siwc-transport-plugin/internal/hostdir"
	"siwc-transport-plugin/internal/pluginconfig"
)

// fakeDirectory 模拟宿主账号目录：SIWC/非 SIWC 账号与身份解析结果可控。
type fakeDirectory struct {
	accounts   map[int64]hostdir.AccountMeta
	identitie  map[int64]*hostdir.OutboundIdentity
	resolveErr error
}

func (f *fakeDirectory) Account(ctx context.Context, accountID int64) (hostdir.AccountMeta, bool) {
	meta, ok := f.accounts[accountID]
	return meta, ok
}

func (f *fakeDirectory) ResolveOutboundIdentity(ctx context.Context, accountID int64) (*hostdir.OutboundIdentity, error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	identity, ok := f.identitie[accountID]
	if !ok {
		return nil, errNoIdentity
	}
	return identity, nil
}

var errNoIdentity = &testError{"identity not found"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{
		accounts: map[int64]hostdir.AccountMeta{
			1: {ID: 1, SIWC: true, Schedulable: true, Status: "active"},
			2: {ID: 2, SIWC: false, Schedulable: true, Status: "active"},
		},
		identitie: map[int64]*hostdir.OutboundIdentity{
			1: {AccountID: 1, Token: "siwc-token", ProxyURL: "http://account-proxy:8080"},
		},
	}
}

func TestForwarder_Decide_SIWCAccountFullRewrite(t *testing.T) {
	forwarder := NewForwarder(newFakeDirectory(), pluginconfig.Default())

	requestHeaders := http.Header{}
	requestHeaders.Set("Content-Type", "application/json")
	requestHeaders.Set("Authorization", "Bearer stale-codex-token")
	requestHeaders.Set("chatgpt-account-id", "acct_1")
	requestHeaders.Set("originator", "codex_cli_rs")

	body := []byte(`{"model":"gpt-6.1-sol","input":[],"store":true}`)
	plan, err := forwarder.Decide(context.Background(), http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses", requestHeaders, body, "http://frame-proxy:1080", 1)
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if !plan.SIWC {
		t.Fatal("account 1 must be detected as SIWC")
	}
	if plan.TargetURL != "https://api.openai.com/v1/responses" {
		t.Fatalf("TargetURL = %q", plan.TargetURL)
	}
	if got := plan.Headers.Get("Authorization"); got != "Bearer siwc-token" {
		t.Fatalf("Authorization = %q", got)
	}
	for _, banned := range []string{"chatgpt-account-id", "originator"} {
		if plan.Headers.Get(banned) != "" {
			t.Fatalf("header %q leaked into SIWC request", banned)
		}
	}
	if !strings.Contains(string(plan.Body), `"store":false`) || !strings.Contains(string(plan.Body), `"stream":true`) {
		t.Fatalf("body rules not applied: %s", plan.Body)
	}
	if plan.ProxyURL != "http://account-proxy:8080" {
		t.Fatalf("ProxyURL = %q (account-resolved proxy must win)", plan.ProxyURL)
	}
}

func TestForwarder_Decide_NonSIWCAccountPassthrough(t *testing.T) {
	forwarder := NewForwarder(newFakeDirectory(), pluginconfig.Default())

	requestHeaders := http.Header{}
	requestHeaders.Set("Authorization", "Bearer codex-token")
	requestHeaders.Set("chatgpt-account-id", "acct_2")

	plan, err := forwarder.Decide(context.Background(), http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses", requestHeaders, nil, "http://frame-proxy:1080", 2)
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	if plan.SIWC {
		t.Fatal("account 2 must stay on passthrough")
	}
	if plan.TargetURL != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("TargetURL = %q", plan.TargetURL)
	}
	if got := plan.Headers.Get("Authorization"); got != "Bearer codex-token" {
		t.Fatalf("Authorization = %q (must be untouched)", got)
	}
	if plan.Headers.Get("chatgpt-account-id") != "acct_2" {
		t.Fatal("codex headers must be preserved for passthrough")
	}
	if plan.ProxyURL != "http://frame-proxy:1080" {
		t.Fatalf("ProxyURL = %q", plan.ProxyURL)
	}
}

func TestForwarder_Decide_UnknownAccountFallsBackToPassthrough(t *testing.T) {
	forwarder := NewForwarder(newFakeDirectory(), pluginconfig.Default())
	plan, err := forwarder.Decide(context.Background(), http.MethodGet,
		"https://chatgpt.com/backend-api/codex/responses", http.Header{}, nil, "", 999)
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	if plan.SIWC {
		t.Fatal("unknown account must degrade to passthrough, not error")
	}
}

func TestForwarder_Decide_SIWCSynthesizesPromptCacheKeyAndSessionHeader(t *testing.T) {
	forwarder := NewForwarder(newFakeDirectory(), pluginconfig.Default())
	requestHeaders := http.Header{}
	requestHeaders.Set("session_id", "host-isolated-value")

	plan, err := forwarder.Decide(context.Background(), http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses", requestHeaders,
		codexTurnBody("fix the bug", "continue"), "", 1)
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	key := gjson.GetBytes(plan.Body, "prompt_cache_key").String()
	if key == "" {
		t.Fatalf("prompt_cache_key must be synthesized: %s", plan.Body)
	}
	if got := plan.Headers.Get("session_id"); got != key {
		t.Fatalf("session_id = %q, want the prompt_cache_key %q", got, key)
	}
	if forwarder.PromptCacheKeySynthesized.Load() != 1 || forwarder.SIWCResponsesRequests.Load() != 1 {
		t.Fatal("synthesized key must be counted")
	}
	samples := forwarder.RecentCacheKeys()
	if len(samples) != 1 || samples[0].Source != CacheKeySourceSynthesized || samples[0].Key8 == "" || samples[0].Items != 9 {
		t.Fatalf("unexpected samples: %+v", samples)
	}
}

func TestForwarder_Decide_SIWCKeepsClientPromptCacheKey(t *testing.T) {
	forwarder := NewForwarder(newFakeDirectory(), pluginconfig.Default())
	body := []byte(`{"model":"m","prompt_cache_key":"client-key","tools":[{"type":"tool_search"}],"input":[{"type":"message","role":"user","content":"hi"}]}`)

	plan, err := forwarder.Decide(context.Background(), http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses", http.Header{}, body, "", 1)
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	if got := gjson.GetBytes(plan.Body, "prompt_cache_key").String(); got != "client-key" {
		t.Fatalf("prompt_cache_key = %q", got)
	}
	if plan.Headers.Get("session_id") != "client-key" {
		t.Fatal("session_id must carry the client key")
	}
	if gjson.GetBytes(plan.Body, "tools.#").Int() != 0 {
		t.Fatalf("tool_search must be stripped: %s", plan.Body)
	}
	if forwarder.PromptCacheKeyFromClient.Load() != 1 || forwarder.ToolSearchStripped.Load() != 1 {
		t.Fatal("client key and tool_search strip must be counted")
	}
}

func TestForwarder_Decide_PassthroughBodyUntouched(t *testing.T) {
	forwarder := NewForwarder(newFakeDirectory(), pluginconfig.Default())
	body := codexTurnBody("fix the bug", "")

	plan, err := forwarder.Decide(context.Background(), http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses", http.Header{}, body, "", 2)
	if err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	if string(plan.Body) != string(body) || plan.Headers.Get("session_id") != "" {
		t.Fatal("non-SIWC accounts must not get a synthesized key or session header")
	}
	if forwarder.SIWCResponsesRequests.Load() != 0 {
		t.Fatal("passthrough requests must not be counted as SIWC")
	}
}

func TestCacheKeyRing_KeepsNewestOldestFirst(t *testing.T) {
	var ring cacheKeyRing
	for index := 0; index < recentCacheKeyLimit+5; index++ {
		ring.add(CacheKeySample{Items: int64(index)})
	}
	samples := ring.snapshot()
	if len(samples) != recentCacheKeyLimit {
		t.Fatalf("len = %d", len(samples))
	}
	if samples[0].Items != 5 || samples[len(samples)-1].Items != recentCacheKeyLimit+4 {
		t.Fatalf("unexpected order: first=%d last=%d", samples[0].Items, samples[len(samples)-1].Items)
	}
}

func TestForwarder_Decide_SIWCIdentityFailureIsDecisionError(t *testing.T) {
	directory := newFakeDirectory()
	directory.resolveErr = &testError{"host unavailable"}
	forwarder := NewForwarder(directory, pluginconfig.Default())

	// 账号 42 不在 fake 目录 → 透传；给账号 1（SIWC）制造身份解析失败。
	_, err := forwarder.Decide(context.Background(), http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses", http.Header{}, nil, "", 1)
	if err == nil {
		t.Fatal("identity resolution failure must surface as decision error (request_sent=false)")
	}
	if !strings.Contains(err.Error(), "host unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
