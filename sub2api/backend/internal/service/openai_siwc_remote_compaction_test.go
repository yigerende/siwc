package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSIWCRemoteCompactionToggleIsolation(t *testing.T) {
	settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	s := &OpenAIGatewayService{settingService: settings}
	ctx := context.Background()
	require.False(t, siwcRemoteCompactionAllowed(s.WithSIWCRemoteCompactionCompatibility(ctx)))
	siwc := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc"}}
	require.Equal(t, 0, openAICompactSupportTier(siwc))
	require.NoError(t, settings.SetSIWCRemoteCompactionCompatibility(ctx, true))
	on := s.WithSIWCRemoteCompactionCompatibility(ctx)
	require.True(t, siwcRemoteCompactionAllowed(on))
	require.Equal(t, 1, openAICompactSupportTier(siwc, true))
	for _, mode := range []string{"auto", "force_on", "force_off"} {
		oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"openai_compact_mode": mode}}
		require.Equal(t, openAICompactSupportTier(oauth), openAICompactSupportTier(oauth, true), "ordinary OAuth must not change")
	}
	require.NoError(t, settings.SetSIWCRemoteCompactionCompatibility(ctx, false))
	require.False(t, siwcRemoteCompactionAllowed(s.WithSIWCRemoteCompactionCompatibility(ctx)))
	require.True(t, siwcRemoteCompactionAllowed(on), "in-flight snapshot remains consistent")
	require.Equal(t, 0, openAICompactSupportTier(siwc, false))
	require.False(t, siwcRemoteCompactionAllowed(ctx), "no mutation to parent request")
}

func TestSIWCRemoteCompactionRequest(t *testing.T) {
	original := []byte(`{"model":"gpt-6.1-sol","stream":true,"store":false,"prompt_cache_key":"coding-17","reasoning":{"effort":"low"},"input":[{"type":"compaction","encrypted_content":"old-state"},{"type":"function_call","call_id":"c1","name":"read_file","arguments":"{\"path\":\"retry.py\"}"},{"type":"function_call_output","call_id":"c1","output":"source"},{"type":"compaction_trigger"}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"context_management":[{"type":"compaction","compact_threshold":300000}]}`)
	req := httptest.NewRequest("POST", "http://test/v1/responses", bytes.NewReader(original))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(original)), nil }
	out, changed, err := prepareSIWCRemoteCompactionRequest(req)
	require.NoError(t, err)
	require.True(t, changed)
	data, err := io.ReadAll(out.Body)
	require.NoError(t, err)
	require.False(t, HasCompactionTriggerInInput(data))
	require.Equal(t, int64(1000), gjson.GetBytes(data, "context_management.0.compact_threshold").Int())
	require.Equal(t, "none", gjson.GetBytes(data, "tool_choice").String())
	for _, path := range []string{"model", "reasoning", "tools", "prompt_cache_key"} {
		require.Equal(t, gjson.GetBytes(original, path).Raw, gjson.GetBytes(data, path).Raw)
	}
	before := gjson.GetBytes(original, "input").Array()
	after := gjson.GetBytes(data, "input").Array()
	require.Len(t, after, 3)
	for i := range after {
		require.JSONEq(t, before[i].Raw, after[i].Raw)
	}
	unchanged, _ := io.ReadAll(req.Body)
	require.Equal(t, original, unchanged, "canonical request cannot be mutated across failover")
	again, _ := out.GetBody()
	replayed, _ := io.ReadAll(again)
	require.Equal(t, data, replayed)
	require.Equal(t, int64(len(data)), out.ContentLength)
	for _, body := range []string{`{"stream":true,"input":[{"role":"user","content":"compaction_trigger"}]}`, `{"stream":true,"context_management":[{"type":"compaction","compact_threshold":2000}],"input":[]}`, `{"stream":false,"input":[{"type":"compaction_trigger"}]}`} {
		r, _ := http.NewRequest("POST", "http://test/v1/responses", strings.NewReader(body))
		result, changed, err := prepareSIWCRemoteCompactionRequest(r)
		require.NoError(t, err)
		require.False(t, changed)
		require.Same(t, r, result)
	}
	legacy, _ := http.NewRequest("POST", "http://test/v1/responses/compact", bytes.NewReader(original))
	out, changed, err = prepareSIWCRemoteCompactionRequest(legacy)
	require.NoError(t, err)
	require.False(t, changed)
	require.Same(t, legacy, out)
}

func siwcTestEvent(value string) string { return "data: " + value + "\n\n" }
func TestSIWCRemoteCompactionResponse(t *testing.T) {
	compact := `{"type":"compaction","id":"cmp_1","encrypted_content":"opaque-test-only"}`
	done := `{"type":"response.output_item.done","item":` + compact + `,"output_index":0}`
	completed := `{"type":"response.completed","response":{"id":"resp_1","created_at":123,"status":"completed","output":[` + compact + `,{"type":"function_call","name":"delete_file","call_id":"danger","arguments":"{}"}],"usage":{"input_tokens":3000,"output_tokens":12,"total_tokens":3012}}}`
	for _, withItem := range []bool{false, true} {
		upstream := siwcTestEvent(`{"type":"response.output_text.delta","delta":"must not leak"}`)
		if withItem {
			upstream += siwcTestEvent(done)
		}
		upstream += siwcTestEvent(completed)
		r := newSIWCRemoteCompactionBody(io.NopCloser(strings.NewReader(upstream)))
		data, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		require.Contains(t, string(data), "response.completed")
		require.NotContains(t, string(data), "must not leak")
		require.NotContains(t, string(data), "delete_file")
		require.Equal(t, 1, strings.Count(string(data), `"type":"response.output_item.done"`))
		require.Contains(t, string(data), `"input_tokens":3000`)
	}
	for _, tc := range []struct{ name, stream, code string }{
		{"short", siwcTestEvent(`{"type":"response.completed","response":{"id":"r","output":[{"type":"message"}],"usage":{"input_tokens":30,"output_tokens":2,"total_tokens":32}}}`), "siwc_compaction_missing"},
		{"early eof", siwcTestEvent(done), "siwc_compaction_incomplete"},
		{"done only", "data: [DONE]\n\n", "siwc_compaction_incomplete"},
		{"corrupt", "data: {bad}\n\n", "siwc_compaction_invalid_response"},
		{"empty", siwcTestEvent(`{"type":"response.completed","response":{"output":[{"type":"compaction","encrypted_content":""}]}}`), "siwc_compaction_invalid_result"},

		{"rate limit", siwcTestEvent(`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","type":"rate_limit_error","message":"Rate limit reached"}}}`), "subscription_sharing_usage_limit_exceeded"},
		{"incomplete", siwcTestEvent(`{"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"max_output_tokens"}}}`), "siwc_compaction_upstream_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSIWCRemoteCompactionBody(io.NopCloser(strings.NewReader(tc.stream)))
			data, err := io.ReadAll(r)
			require.NoError(t, err)
			require.Contains(t, string(data), "response.failed")
			require.Contains(t, string(data), tc.code)
			require.NotContains(t, string(data), `"type":"response.completed"`)
			require.NotContains(t, string(data), "response.output_item.done")
			if tc.name == "short" {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "data:") {
						require.False(t, openAIStreamFailedEventShouldFailover([]byte(strings.TrimPrefix(line, "data: ")), "SIWC did not produce exactly one encrypted compaction item"))
					}
				}
			}
		})
	}
}

func TestSIWCRemoteCompactionResponseLargeAndClose(t *testing.T) {
	source := &siwcCloseReader{Reader: strings.NewReader("data: " + strings.Repeat("x", 16*1024*1024) + "\n")}
	adapted := newSIWCRemoteCompactionBody(source)
	data, err := io.ReadAll(adapted)
	require.NoError(t, err)
	require.Contains(t, string(data), "siwc_compaction_stream_error")
	require.NoError(t, adapted.Close())
	require.True(t, source.closed)
}

type siwcCloseReader struct {
	io.Reader
	closed bool
}

func (r *siwcCloseReader) Close() error { r.closed = true; return nil }

func TestSIWCRemoteCompactionFailureOutputIsValidJSON(t *testing.T) {
	r := &siwcRemoteCompactionBody{}
	out := r.failure("code", "message", nil)
	require.True(t, json.Valid(bytes.TrimSpace(bytes.TrimPrefix(out, []byte("data: ")))))
}

func TestSIWCRemoteCompactionKeepsLatestState(t *testing.T) {
	input := siwcTestEvent(`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"old"}}`) + siwcTestEvent(`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"new"}}`) + siwcTestEvent(`{"type":"response.completed","response":{"id":"r","output":[{"type":"compaction","encrypted_content":"old"},{"type":"message"},{"type":"compaction","encrypted_content":"new"}]}}`)
	body := newSIWCRemoteCompactionBody(io.NopCloser(strings.NewReader(input)))
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"encrypted_content":"old"`)
	require.Contains(t, string(data), `"encrypted_content":"new"`)
	require.Equal(t, 1, strings.Count(string(data), `"type":"response.output_item.done"`))
}
func TestSIWCRemoteCompactionNeverChangesOrdinaryTransport(t *testing.T) {
	upstream := &pluginRoutingHTTPUpstream{}
	s := &OpenAIGatewayService{httpUpstream: upstream}
	for _, on := range []bool{false, true, false} {
		ctx := context.WithValue(context.Background(), siwcRemoteCompactionContextKey{}, on)
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.com/v1/responses", strings.NewReader(`{"input":[{"type":"compaction_trigger"}],"stream":true}`))
		resp, err := s.doOpenAIUpstream(req, "", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth})
		require.NoError(t, err)
		out, _ := io.ReadAll(resp.Body)
		require.Equal(t, "legacy", string(out))
	}
	require.Equal(t, 3, upstream.doCalls)
}

func TestSIWCRemoteCompactionScheduler(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, on := range []bool{false, true, false} {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			svc := newOpenAICompactionSchedulerTestService([]Account{{ID: 71291, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"auth_mode": "siwc", "access_token": "test", "expires_at": "2099-01-01T00:00:00Z"}, Extra: map[string]any{"openai_responses_supported": true}}}, advanced)
			ctx := context.WithValue(context.Background(), siwcRemoteCompactionContextKey{}, on)
			group := int64(91291)
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "", "gpt-5.4", nil, OpenAIUpstreamTransportAny, true)
			if on {
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Equal(t, int64(71291), selection.Account.ID)
			} else {
				require.ErrorIs(t, err, ErrNoAvailableCompactAccounts)
			}
		}
	}
}
