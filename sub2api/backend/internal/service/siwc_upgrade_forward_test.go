package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the real host Forward pipeline separately from the signed plugin
// process tests. Only the outbound transport is simulated; no credential or
// external endpoint is contacted.
func TestSIWCUpgradeForwardUserPayloads(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/passthrough=%t", stream, passthrough), func(t *testing.T) {
				payload := map[string]any{
					"model": "gpt-5.5", "stream": stream, "instructions": "Write code using the supplied tools.",
					"prompt_cache_key": "siwc-upgrade-session", "reasoning": map[string]any{"effort": "high"},
					"tools": []any{
						map[string]any{"type": "function", "name": "browser_open", "parameters": map[string]any{"type": "object"}},
						map[string]any{"type": "function", "name": "apply_patch", "parameters": map[string]any{"type": "object"}},
						map[string]any{"type": "web_search"},
					},
					"input": []any{
						map[string]any{"role": "user", "content": "Use the browser, inspect the image, then update the code."},
						map[string]any{"type": "function_call", "name": "browser_open", "call_id": "call-1", "arguments": `{"url":"https://example.invalid"}`},
						map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "fixture page text"},
						map[string]any{"role": "user", "content": []any{
							map[string]any{"type": "input_image", "image_url": "data:image/png;base64,Zml4dHVyZQ=="},
							map[string]any{"type": "input_file", "filename": "fixture.pdf", "file_data": "data:application/pdf;base64,Zml4dHVyZQ=="},
						}},
					},
				}
				body, err := json.Marshal(payload)
				require.NoError(t, err)
				const wire = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_siwc_fixture\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"function_call\",\"name\":\"apply_patch\",\"call_id\":\"call-2\",\"arguments\":\"{}\"}],\"usage\":{\"input_tokens\":120,\"input_tokens_details\":{\"cached_tokens\":100},\"output_tokens\":5}}}\n\n"
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
				account := newOpenAIOAuthNamespaceTestAccount()
				account.Credentials["auth_mode"] = OpenAIAuthModeSIWC
				account.Extra = map[string]any{"auth_provider": "siwc", "openai_passthrough": passthrough}
				require.Equal(t, passthrough, account.IsOpenAIPassthroughEnabled())
				c := newOpenAIRejectedFieldTestContext(body)
				result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 120, result.Usage.InputTokens)
				require.Equal(t, 100, result.Usage.CacheReadInputTokens)
				require.Equal(t, 5, result.Usage.OutputTokens)
				require.Equal(t, stream, result.Stream)
				require.Len(t, upstream.bodies, 1, "a successful request must not be replayed")
				actual := upstream.bodies[0]
				require.Contains(t, string(actual), "data:image/png;base64,Zml4dHVyZQ==")
				require.Contains(t, string(actual), "data:application/pdf;base64,Zml4dHVyZQ==")
				require.Equal(t, "fixture page text", gjson.GetBytes(actual, `input.#(type=="function_call_output").output`).String())
				require.True(t, gjson.GetBytes(actual, `tools.#(name=="browser_open")`).Exists())
				require.True(t, gjson.GetBytes(actual, `tools.#(name=="apply_patch")`).Exists())
				require.True(t, gjson.GetBytes(actual, `tools.#(type=="web_search")`).Exists())
				require.NotEmpty(t, gjson.GetBytes(actual, "prompt_cache_key").String())
			})
		}
	}
}
