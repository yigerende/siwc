//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_OpenAIOAuthCustomPrompt(t *testing.T) {
	for _, tc := range []struct{ name, prompt, effort, wantPrompt, wantEffort string }{
		{"custom", "Give only the answer: 12+17", "xhigh", "Give only the answer: 12+17", "xhigh"},
		{"legacy", "", "", "hi", ""},
		{"whitespace", "  ", "", "hi", ""},
		{"unsupported_effort", "hello", "invalid", "hello", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, recorder := newTestContext()
			resp := newJSONResponse(200, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"29\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
			upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
			svc := &AccountTestService{httpUpstream: upstream}
			account := &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "test-token"}}
			err := svc.testOpenAIAccountConnection(ctx, account, "gpt-6-astra", tc.prompt, "", AccountTestOptions{ReasoningEffort: tc.effort})
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			body, err := io.ReadAll(upstream.requests[0].Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantPrompt, gjson.GetBytes(body, "input.0.content.0.text").String())
			require.Equal(t, tc.wantEffort, gjson.GetBytes(body, "reasoning.effort").String())
			require.Equal(t, "Bearer test-token", upstream.requests[0].Header.Get("Authorization"))
			require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", upstream.requests[0].URL.String())
			require.Contains(t, recorder.Body.String(), `"type":"content","text":"29"`)
			require.Equal(t, strings.TrimSpace(tc.prompt) != "", strings.Contains(recorder.Body.String(), `"custom_prompt":true`))
		})
	}
}
