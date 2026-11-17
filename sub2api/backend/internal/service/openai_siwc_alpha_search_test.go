package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func siwcAlphaFixture(text string) string {
	response := map[string]any{"status": "completed", "output": []any{
		map[string]any{"type": "web_search_call", "status": "completed", "action": map[string]any{"type": "search", "sources": []any{map[string]any{"url": "https://go.dev/dl/"}}}},
		map[string]any{"type": "message", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": "Searching..."}}},
		map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{map[string]any{"type": "url_citation", "url": "https://go.dev/dl/", "title": "Go downloads"}}}}},
	}}
	data, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
	return "data: " + string(data) + "\n\ndata: [DONE]\n\n"
}

func alphaFixtureAccount(mode string) *Account {
	return &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 10,
		Credentials: map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account", "auth_mode": mode}}
}

func alphaFixtureContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/alpha/search", bytes.NewReader(body))
	return c, w
}

func TestSIWCAlphaSearchRoutingIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"id":"session","model":"gpt-6.1-sol","commands":{"search_query":[{"q":"Go releases"}]}}`)
	for _, tc := range []struct {
		name, mode       string
		enabled, adapted bool
	}{
		{"siwc_off", OpenAIAuthModeSIWC, false, false},
		{"siwc_on", OpenAIAuthModeSIWC, true, true},
		{"oauth_off", "", false, false},
		{"oauth_on", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
			require.NoError(t, settings.SetSIWCAlphaSearchCompatibility(context.Background(), tc.enabled))
			wire := `{"output":"native result","encrypted_output":"opaque"}`
			if tc.adapted {
				wire = siwcAlphaFixture("Go result")
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
			s := &OpenAIGatewayService{cfg: &config.Config{}, settingService: settings, httpUpstream: upstream}
			c, w := alphaFixtureContext(body)
			result, err := s.ForwardAlphaSearch(context.Background(), c, alphaFixtureAccount(tc.mode), body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, result.WebSearchCalls, "preserve existing per-success search billing")
			if tc.adapted {
				require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String(), "the SIWC plugin rewrites this to api.openai.com/v1/responses")
				require.Equal(t, "/v1/responses", result.UpstreamEndpoint)
				require.Equal(t, "web_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
				require.Equal(t, "https://go.dev/dl/", gjson.Get(w.Body.String(), "results.0.ref_id").String())
				require.NotContains(t, gjson.Get(w.Body.String(), "output").String(), "Searching...")
			} else {
				require.Equal(t, chatgptCodexAlphaSearchURL, upstream.lastReq.URL.String())
				require.Equal(t, body, upstream.lastBody, "off/ordinary OAuth wire remains byte-for-byte unchanged")
				require.Equal(t, wire, w.Body.String())
			}
		})
	}
}

func TestSIWCAlphaSearchBodyPreservesCommandsAndControls(t *testing.T) {
	long := strings.Repeat("中文 query ", 2000)
	input := map[string]any{"model": "source", "commands": map[string]any{"search_query": []any{map[string]any{"q": long, "domains": []string{"go.dev"}, "recency": 7}}, "open": []any{map[string]any{"ref_id": "https://go.dev/dl/", "lineno": 12}}, "find": []any{map[string]any{"ref_id": "https://go.dev/dl/", "pattern": "stable"}}}, "settings": map[string]any{"external_web_access": false, "search_context_size": "high", "filters": map[string]any{"allowed_domains": []string{"go.dev"}}, "user_location": map[string]any{"type": "approximate", "country": "US"}}, "max_output_tokens": 2500, "reasoning": map[string]any{"effort": "low", "context": "all_turns"}, "input": "previous results", "prompt_cache_key": "not-forwarded"}
	body, _ := json.Marshal(input)
	out, err := buildSIWCAlphaSearchBody(body, "mapped-model")
	require.NoError(t, err)
	prompt := gjson.GetBytes(out, "input.0.content.0.text").String()
	require.Contains(t, prompt, long)
	require.Contains(t, prompt, `"find"`)
	require.Contains(t, prompt, "https://go.dev/dl/")
	require.Equal(t, "mapped-model", gjson.GetBytes(out, "model").String())
	require.Equal(t, "web_search", gjson.GetBytes(out, "tool_choice.type").String())
	require.False(t, gjson.GetBytes(out, "tools.0.external_web_access").Bool())
	require.True(t, gjson.GetBytes(out, "tools.0.external_web_access").Exists())
	require.Equal(t, "go.dev", gjson.GetBytes(out, "tools.0.filters.allowed_domains.0").String())
	require.Equal(t, "low", gjson.GetBytes(out, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(out, "reasoning.context").Exists())
	require.False(t, gjson.GetBytes(out, "prompt_cache_key").Exists())
	require.Equal(t, int64(2500), gjson.GetBytes(out, "max_output_tokens").Int())
}

func TestSIWCAlphaSearchCompletionAndFailures(t *testing.T) {
	valid := siwcAlphaFixture("结果")
	for name, wire := range map[string]string{"sse": valid, "crlf": strings.ReplaceAll(valid, "\n", "\r\n"), "json": `{"object":"response","status":"completed","output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":"no matching sources found"}]}]}`} {
		t.Run(name, func(t *testing.T) {
			out, err := siwcAlphaSearchResponse([]byte(wire))
			require.NoError(t, err)
			require.True(t, gjson.GetBytes(out, "output").String() != "")
		})
	}
	for name, wire := range map[string]string{
		"empty": "", "done": "data: [DONE]\n\n", "delta": "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		"malformed":  valid + "data: {invalid}\n\n",
		"no_tool":    `{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"I think..."}]}]}`,
		"failed":     `{"type":"response.failed","response":{"status":"failed","output":[{"secret":"not logged"}],"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"quota exceeded"}}}`,
		"incomplete": `{"type":"response.incomplete","response":{"status":"incomplete"}}`,
		"bare_error": `{"type":"error","error":{"code":"server_error","message":"Please retry"}}`,
		"bad_status": `{"type":"response.completed","response":{"status":"failed"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := siwcAlphaSearchResponse([]byte(wire))
			require.Error(t, err)
			require.Nil(t, out)
			if e, ok := err.(*siwcAlphaSearchFailure); ok {
				require.NotContains(t, string(e.payload), "not logged")
			}
		})
	}
}

func TestSIWCAlphaSearchErrorsNotBillable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{400, 401, 403, 429, 500, 502, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
			require.NoError(t, settings.SetSIWCAlphaSearchCompatibility(context.Background(), true))
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"upstream error"}}`))}}
			s := &OpenAIGatewayService{cfg: &config.Config{}, settingService: settings, httpUpstream: upstream}
			body := []byte(`{"model":"gpt-6.1-sol","commands":{"search_query":[{"q":"Go"}]}}`)
			c, w := alphaFixtureContext(body)
			result, err := s.ForwardAlphaSearch(context.Background(), c, alphaFixtureAccount(OpenAIAuthModeSIWC), body)
			require.Nil(t, result)
			if err != nil {
				require.False(t, c.Writer.Written())
			} else {
				require.Equal(t, status, w.Code)
			}
		})
	}
	for _, wire := range []string{"data: [DONE]\n\n", `{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"usage limit exceeded"}}}`} {
		settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
		require.NoError(t, settings.SetSIWCAlphaSearchCompatibility(context.Background(), true))
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}}
		s := &OpenAIGatewayService{cfg: &config.Config{}, settingService: settings, httpUpstream: upstream}
		body := []byte(`{"model":"gpt-6.1-sol"}`)
		c, _ := alphaFixtureContext(body)
		result, err := s.ForwardAlphaSearch(context.Background(), c, alphaFixtureAccount(OpenAIAuthModeSIWC), body)
		require.Nil(t, result)
		require.Error(t, err)
		require.False(t, c.Writer.Written())
		if strings.Contains(wire, "usage_limit_exceeded") {
			var fail *UpstreamFailoverError
			require.ErrorAs(t, err, &fail)
			require.Equal(t, 429, fail.StatusCode)
		}
	}
}

func TestSIWCAlphaSearchConcurrentIsolation(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := strings.Repeat("x", i+1)
			wire := siwcAlphaFixture(name)
			for n := 0; n < 20; n++ {
				out, err := siwcAlphaSearchResponse([]byte(wire))
				if err != nil || !strings.HasPrefix(gjson.GetBytes(out, "output").String(), name+"\n\n") {
					t.Errorf("request %d response mixed or failed: %v", i, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestSIWCAlphaSearchTransportAndLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, name := range []string{"proxy", "canceled", "read_error", "too_large"} {
		t.Run(name, func(t *testing.T) {
			settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
			require.NoError(t, settings.SetSIWCAlphaSearchCompatibility(context.Background(), true))
			reader := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(siwcAlphaFixture("result"))}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}}
			cfg := &config.Config{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				cancel()
				upstream.err = context.Canceled
			}
			if name == "read_error" {
				reader.Reader = passthroughErrReadCloser{err: io.ErrUnexpectedEOF}
			}
			if name == "too_large" {
				cfg.Gateway.UpstreamResponseReadMaxBytes = 32
			}
			s := &OpenAIGatewayService{cfg: cfg, settingService: settings, httpUpstream: upstream}
			a := alphaFixtureAccount(OpenAIAuthModeSIWC)
			proxyID := int64(7)
			a.ProxyID = &proxyID
			a.Proxy = &Proxy{Protocol: "http", Host: "fixture.invalid", Port: 8080}
			body := []byte(`{"model":"gpt-6.1-sol","commands":{"search_query":[{"q":"Go"}]}}`)
			c, _ := alphaFixtureContext(body)
			c.Request = c.Request.WithContext(ctx)
			result, err := s.ForwardAlphaSearch(ctx, c, a, body)
			require.Equal(t, "http://fixture.invalid:8080", upstream.lastProxyURL)
			require.Equal(t, ctx.Err(), upstream.lastReq.Context().Err())
			if name == "proxy" {
				require.NoError(t, err)
				require.NotNil(t, result)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
			}
			if name != "canceled" {
				require.True(t, reader.closed)
			}
		})
	}
}
