package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const toolSearchRejected = `{"error":{"code":"subscription_sharing_unsupported_capability","param":"input","type":"invalid_request_error"}}`
const toolSearchTestBody = `{"model":"gpt-6.1-sol","stream":true,"store":false,"prompt_cache_key":"same-session","reasoning":{"effort":"low"},"tools":[{"type":"tool_search"},{"type":"custom","name":"patch","format":{"type":"text"}}],"input":[{"role":"user","content":"synthetic task"},{"type":"tool_search_call","call_id":"search1","arguments":{"query":"files"}},{"type":"tool_search_output","call_id":"search1","tools":[]}]}`

func TestSIWCToolSearchWSBudgetPerTurn(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.Background()
	one := bindSIWCToolSearchWSTurn(ctx, c, 1).Value(siwcToolSearchRetryKey{}).(*siwcToolSearchRetryState)
	require.True(t, one.used.CompareAndSwap(false, true))
	retry := bindSIWCToolSearchWSTurn(ctx, c, 1).Value(siwcToolSearchRetryKey{}).(*siwcToolSearchRetryState)
	require.Same(t, one, retry)
	require.False(t, retry.used.CompareAndSwap(false, true))
	two := bindSIWCToolSearchWSTurn(ctx, c, 2).Value(siwcToolSearchRetryKey{}).(*siwcToolSearchRetryState)
	require.NotSame(t, one, two)
	require.True(t, two.used.CompareAndSwap(false, true))
}

func toolSearchTestRequest(t *testing.T, body string) (*http.Request, *gin.Context, *Account) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req, err := http.NewRequestWithContext(c.Request.Context(), "POST", "https://upstream.invalid/v1/responses", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-only")
	req.Header.Set("session_id", "same-session")
	req.Host = "upstream.invalid"
	a := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc"}}
	return bindSIWCToolSearchRetry(req, c, a), c, a
}
func toolSearchResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestSIWCToolSearchRetrySuccessAndSharedBudget(t *testing.T) {
	settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	require.NoError(t, settings.SetSIWCToolSearchCompatibility(context.Background(), true))
	s := &OpenAIGatewayService{settingService: settings}
	req, c, a := toolSearchTestRequest(t, toolSearchTestBody)
	calls := 0
	run := func(next *http.Request) (*http.Response, error) {
		calls++
		b, e := io.ReadAll(next.Body)
		require.NoError(t, e)
		require.Equal(t, req.URL, next.URL)
		require.Equal(t, req.Host, next.Host)
		require.Equal(t, req.Header, next.Header)
		if calls == 1 {
			require.Equal(t, toolSearchTestBody, string(b))
			return toolSearchResponse(400, toolSearchRejected), nil
		}
		require.False(t, hasSIWCToolSearchCall(b))
		for _, key := range []string{"model", "reasoning", "store", "prompt_cache_key"} {
			require.Equal(t, gjson.Get(toolSearchTestBody, key).Raw, gjson.GetBytes(b, key).Raw)
		}
		require.Equal(t, "custom", gjson.GetBytes(b, "tools.1.type").String())
		return toolSearchResponse(200, `{"status":"completed","output":[{"type":"function_call","name":"tool_search","id":"fc_new","call_id":"search2","arguments":"{\"query\":\"more\"}"}],"usage":{"input_tokens":123,"output_tokens":10}}`), nil
	}
	resp, err := s.doSIWCToolSearchRetry(req, a, run)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "tool_search_call", gjson.GetBytes(data, "output.0.type").String())
	require.Equal(t, "client", gjson.GetBytes(data, "output.0.execution").String())
	require.Equal(t, int64(123), gjson.GetBytes(data, "usage.input_tokens").Int())
	canonical, _ := req.GetBody()
	original, _ := io.ReadAll(canonical)
	require.Equal(t, toolSearchTestBody, string(original))
	next, _ := http.NewRequestWithContext(c.Request.Context(), "POST", req.URL.String(), strings.NewReader(toolSearchTestBody))
	other := *a
	other.ID = 8
	next = bindSIWCToolSearchRetry(next, c, &other)
	extra := 0
	_, err = s.doSIWCToolSearchRetry(next, &other, func(*http.Request) (*http.Response, error) {
		extra++
		return toolSearchResponse(400, toolSearchRejected), nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, extra, "account failover cannot reset budget")
}

func TestSIWCToolSearchRetryPluginHeaderCasing(t *testing.T) {
	for _, headerName := range []string{"Content-Type", "content-type", "CONTENT-TYPE", "application-json", "missing"} {
		for _, streaming := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/stream=%t", headerName, streaming), func(t *testing.T) {
				settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
				require.NoError(t, settings.SetSIWCToolSearchCompatibility(context.Background(), true))
				s := &OpenAIGatewayService{settingService: settings}
				body := strings.Replace(toolSearchTestBody, `"stream":true`, fmt.Sprintf(`"stream":%t`, streaming), 1)
				req, _, account := toolSearchTestRequest(t, body)
				calls := 0
				response, err := s.doSIWCToolSearchRetry(req, account, func(*http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return toolSearchResponse(400, toolSearchRejected), nil
					}
					// Plugin RPC preserves HTTP/2 lowercase headers. SIWC also
					// forces SSE upstream when the client requests non-stream JSON.
					headers := http.Header{headerName: {"text/event-stream; charset=utf-8"}, "content-length": {"999"}}
					if headerName == "application-json" {
						headers = http.Header{"Content-Type": {"application/json"}}
					} else if headerName == "missing" {
						headers = http.Header{}
					}
					return &http.Response{StatusCode: 200,
						Header: headers,
						Body: io.NopCloser(strings.NewReader("event: response.completed\ndata: " +
							`{"type":"response.completed","sequence_number":0,"response":{"status":"completed","output":[{"type":"function_call","id":"fc_next","call_id":"call_next","name":"tool_search","arguments":"{\"query\":\"next\"}"}],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}` + "\n\n"))}, nil
				})
				require.NoError(t, err)
				require.Equal(t, 2, calls)
				data, err := io.ReadAll(response.Body)
				require.NoError(t, err, "SSE must not be decoded as an entire JSON document")
				require.Contains(t, string(data), `"type":"tool_search_call"`)
				require.True(t, isEventStreamResponse(response.Header), "downstream JSON/SSE handlers must recognize the stream")
				require.NotContains(t, response.Header, "content-length")
				require.Equal(t, int64(-1), response.ContentLength)
			})
		}
	}
}

func TestSIWCToolSearchRetryGates(t *testing.T) {
	for _, name := range []string{"off", "oauth_on", "oauth_off", "already_written", "canceled", "no_call", "output_only", "wrong_code", "tools_param", "429", "502", "transport_error", "stream_error", "malformed_error", "large_error", "second_attempt_fails", "proxy_name_collision"} {
		t.Run(name, func(t *testing.T) {
			settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
			require.NoError(t, settings.SetSIWCToolSearchCompatibility(context.Background(), name != "off" && name != "oauth_off"))
			s := &OpenAIGatewayService{settingService: settings}
			body := toolSearchTestBody
			if name == "no_call" {
				body = `{"input":[{"role":"user","content":"hi"}]}`
			}
			if name == "output_only" {
				body = `{"input":[{"type":"tool_search_output","call_id":"x","tools":[]}]}`
			}
			if name == "proxy_name_collision" {
				body = strings.Replace(body, `{"type":"custom","name":"patch","format":{"type":"text"}}`, `{"type":"function","name":"tool_search"}`, 1)
			}
			req, c, a := toolSearchTestRequest(t, body)
			if strings.HasPrefix(name, "oauth") {
				a.Credentials = nil
			}
			if name == "already_written" {
				_, _ = c.Writer.Write([]byte("started"))
			}
			status := 400
			errorBody := toolSearchRejected
			var transportErr error
			switch name {
			case "wrong_code":
				errorBody = `{"error":{"code":"invalid_value","param":"input"}}`
			case "tools_param":
				errorBody = strings.Replace(errorBody, `"input"`, `"tools"`, 1)
			case "429":
				status = 429
			case "502":
				status = 502
			case "malformed_error":
				errorBody = "bad json"
			case "large_error":
				errorBody = strings.Repeat("x", 65538)
			case "transport_error":
				transportErr = errors.New("network failure")
			case "stream_error":
				status = 200
				errorBody = "data: " + toolSearchRejected + "\n\n"
			}
			calls := 0
			run := func(next *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					got, readErr := io.ReadAll(next.Body)
					require.NoError(t, readErr)
					require.Equal(t, body, string(got), "first request must retain exact bytes in all modes")
					require.Equal(t, "same-session", next.Header.Get("session_id"))
				}
				if name == "canceled" {
					ctx, cancel := context.WithCancel(c.Request.Context())
					cancel()
					c.Request = c.Request.WithContext(ctx)
				}
				return toolSearchResponse(status, errorBody), transportErr
			}
			resp, err := s.doSIWCToolSearchRetry(req, a, run)
			if transportErr != nil {
				require.ErrorIs(t, err, transportErr)
			} else {
				require.NoError(t, err)
			}
			want := 1
			if name == "second_attempt_fails" {
				want = 2
			}
			require.Equal(t, want, calls)
			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, errorBody, string(got), "unmatched or failed retry must retain error evidence")
		})
	}
}

func TestSIWCToolSearchStreamingAndClose(t *testing.T) {
	source := &siwcCloseReader{Reader: strings.NewReader("\n: heartbeat\n\n\n" +
		"event: response.output_item.added\nid: e1\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_s\",\"call_id\":\"c\",\"name\":\"tool_search\",\"arguments\":\"\"}}\n\n" +
		siwcTestEvent(`{"type":"response.function_call_arguments.delta","sequence_number":1,"output_index":0,"item_id":"fc_s","delta":"{\"query\":\"hello\"}"}`) +
		siwcTestEvent(`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"fc_s","call_id":"c","name":"tool_search","arguments":"{\"query\":\"hello\"}"}}`) +
		siwcTestEvent(`{"type":"response.completed","sequence_number":3,"response":{"id":"r","status":"completed","output":[{"type":"function_call","id":"fc_s","call_id":"c","name":"tool_search","arguments":"{\"query\":\"hello\"}"}],"usage":{"output_tokens":8}}}`) + "data: [DONE]\n\n")}
	b := newSIWCToolSearchBody(source, true)
	data, err := io.ReadAll(b)
	require.NoError(t, err)
	require.Contains(t, string(data), "heartbeat")
	require.Contains(t, string(data), "id: e1")
	require.Contains(t, string(data), "[DONE]")
	require.NotContains(t, string(data), "function_call_arguments.delta")
	require.Contains(t, string(data), `"execution":"client"`)
	require.Contains(t, string(data), `"query":"hello"`)
	require.NoError(t, b.Close())
	require.True(t, source.closed)
	for _, raw := range []string{"data: {broken}\n\n", "data: " + strings.Repeat("x", 16*1024*1024) + "\n\n"} {
		r := newSIWCToolSearchBody(io.NopCloser(strings.NewReader(raw)), true)
		_, err := io.ReadAll(r)
		require.Error(t, err)
	}
}

func TestSIWCToolSearchRetryWithCompaction(t *testing.T) {
	settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	require.NoError(t, settings.SetSIWCToolSearchCompatibility(context.Background(), true))
	s := &OpenAIGatewayService{settingService: settings}
	body := strings.Replace(toolSearchTestBody, `"input":[`, `"input":[{"type":"compaction_trigger"},`, 1)
	req, _, a := toolSearchTestRequest(t, body)
	calls := 0
	resp, err := s.doSIWCToolSearchRetry(req, a, func(r *http.Request) (*http.Response, error) {
		calls++
		adapted, yes, e := prepareSIWCRemoteCompactionRequest(r)
		require.NoError(t, e)
		require.True(t, yes)
		data, _ := io.ReadAll(adapted.Body)
		require.False(t, HasCompactionTriggerInInput(data))
		if calls == 1 {
			require.True(t, hasSIWCToolSearchCall(data))
			return toolSearchResponse(400, toolSearchRejected), nil
		}
		require.False(t, hasSIWCToolSearchCall(data))
		response := toolSearchResponse(200, siwcTestEvent(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":5000,"output_tokens":100,"total_tokens":5100}}}`))
		response.Header.Set("Content-Type", "text/event-stream")
		response.Body = newSIWCRemoteCompactionBody(response.Body)
		return response, nil
	})
	require.NoError(t, err)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Contains(t, string(data), `"encrypted_content":"opaque"`)
	require.Contains(t, string(data), `"input_tokens":5000`)
}

func TestSIWCToolSearchConcurrentRequests(t *testing.T) {
	settings := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	require.NoError(t, settings.SetSIWCToolSearchCompatibility(context.Background(), true))
	s := &OpenAIGatewayService{settingService: settings}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf("req%d", i)
			body := strings.ReplaceAll(toolSearchTestBody, "search1", marker)
			req, _, a := toolSearchTestRequest(t, body)
			calls := 0
			response, err := s.doSIWCToolSearchRetry(req, a, func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return toolSearchResponse(400, toolSearchRejected), nil
				}
				b, _ := io.ReadAll(r.Body)
				if !bytes.Contains(b, []byte(marker)) {
					t.Error("request crossed")
				}
				return toolSearchResponse(200, fmt.Sprintf(`{"output":[{"type":"function_call","name":"tool_search","call_id":"%s","arguments":"{}"}]}`, marker)), nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			b, err := io.ReadAll(response.Body)
			if err != nil || calls != 2 || gjson.GetBytes(b, "output.0.call_id").String() != marker {
				t.Error("response crossed or wrong retry count")
			}
		}(i)
	}
	wg.Wait()
}
