package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This directory contains synthetic identities only. The test launches the
// unchanged, signed SIWC plugin and forwards solely to a loopback HTTP server.
// Direct HTTPS endpoint rewriting is covered by the plugin's rewrite tests.
type siwcUpgradeDirectory struct{}

func (siwcUpgradeDirectory) ListPluginAccounts(context.Context, PluginAccountScope, string, string) ([]PluginAccountInfo, error) {
	var infos []PluginAccountInfo
	for _, id := range []int64{42, 43, 44} {
		extra := map[string]any{"auth_provider": "siwc"}
		if id == 44 {
			extra = nil
		}
		infos = append(infos, accountToPluginInfo(&Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, Extra: extra}))
	}
	return infos, nil
}

func (siwcUpgradeDirectory) ResolvePluginOutboundIdentity(_ context.Context, _ PluginAccountScope, id int64) (*PluginOutboundIdentity, error) {
	return &PluginOutboundIdentity{AccountID: id, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth,
		Token: fmt.Sprintf("synthetic-%d", id)}, nil
}

func TestSIWCUpgradePluginProcessRegression(t *testing.T) {
	packagePath := os.Getenv("SIWC_UPGRADE_TEST_PLUGIN")
	if packagePath == "" {
		t.Skip("requires locally built SIWC plugin package; no real account is used")
	}
	root := t.TempDir()
	cfg := testPluginConfig(root, false)
	cfg.Plugins.TrustedPublishers = map[string]string{"siwc-upgrade-test": os.Getenv("SUB2API_TEST_TRUSTED_PUBLIC_KEY")}
	file, err := os.Open(packagePath)
	require.NoError(t, err)
	defer file.Close()
	installation, err := NewPluginPackageInstaller(cfg, PluginHostInfo{Version: "0.2.22", BuildType: "release"}).Install(context.Background(), file, nil)
	require.NoError(t, err)
	require.Equal(t, PluginSignatureTrusted, installation.SignatureStatus)
	host := newPluginHostServiceServer(installation.PluginKey, newFakePluginKVStore(), siwcUpgradeDirectory{},
		newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}))
	runtime, err := startPluginRuntime(context.Background(), installation, 15*time.Second, filepath.Join(root, "runtime"), host)
	require.NoError(t, err)
	defer runtime.kill()
	require.NoError(t, runtime.validateAndApplyConfig(context.Background(), []byte(`{"max_streams_per_connection":2}`)))

	const completed = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"model\":\"fixture-model\",\"output\":[{\"type\":\"function_call\",\"name\":\"read_file\",\"call_id\":\"call-1\",\"arguments\":\"{}\"}],\"usage\":{\"input_tokens\":120,\"input_tokens_details\":{\"cached_tokens\":100},\"output_tokens\":5}}}\n\n"
	const quotaError = `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"Subscription Sharing usage limit"}}`
	var arrived atomic.Int64
	gate := make(chan struct{})
	observed := make(chan []byte, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		assert.NoError(t, readErr)
		scenario := r.URL.Query().Get("scenario")
		if scenario == "ordinary" {
			assert.Equal(t, "Bearer ordinary-token", r.Header.Get("Authorization"))
			assert.Equal(t, "unchanged", r.Header.Get("Chatgpt-Account-Id"))
			assert.Equal(t, `{"stream":false,"store":true}`, string(body))
		} else {
			assert.Equal(t, "Bearer synthetic-"+r.URL.Query().Get("account"), r.Header.Get("Authorization"))
			assert.Empty(t, r.Header.Get("Chatgpt-Account-Id"))
			assert.True(t, gjson.GetBytes(body, "stream").Bool())
			assert.False(t, gjson.GetBytes(body, "store").Bool())
			assert.Equal(t, gjson.GetBytes(body, "prompt_cache_key").String(), r.Header.Get("session_id"))
		}
		switch scenario {
		case "history":
			observed <- body
		case "concurrent":
			assert.Equal(t, "read_file", gjson.GetBytes(body, "tools.0.name").String())
			assert.Equal(t, "tool result", gjson.GetBytes(body, "input.1.output").String())
			assert.Equal(t, "data:image/png;base64,c3ludGhldGlj", gjson.GetBytes(body, "input.2.content.0.image_url").String())
			assert.Equal(t, "data:application/pdf;base64,c3ludGhldGlj", gjson.GetBytes(body, "input.2.content.1.file_data").String())
			if arrived.Add(1) == 40 {
				close(gate)
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		case "quota":
			w.WriteHeader(400)
			_, _ = io.WriteString(w, quotaError)
			return
		case "upstream_error":
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "original-upstream-error")
			return
		case "400", "401", "403", "429", "500", "502", "503":
			var status int
			_, _ = fmt.Sscan(scenario, &status)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"code":"fixture_error","message":"original error"}}`)
			return
		case "cancel":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completed)
	}))
	defer server.Close()

	forward := func(ctx context.Context, scenario string, id int64, body string) (*http.Response, error) {
		req, reqErr := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/v1/responses?scenario=%s&account=%d", server.URL, scenario, id), strings.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Authorization", "Bearer ordinary-token")
		req.Header.Set("Chatgpt-Account-Id", "unchanged")
		if !runtime.beginRequest() {
			return nil, fmt.Errorf("runtime rejected request")
		}
		resp, callErr := runtime.roundTrip(ctx, req, "", &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 100})
		if callErr != nil {
			runtime.finishRequest()
		}
		return resp, callErr
	}
	body := `{"model":"fixture-model","store":true,"stream":false,"prompt_cache_key":"stable-session","tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],"input":[{"role":"user","content":"write code"},{"type":"function_call_output","call_id":"call-1","output":"tool result"},{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,c3ludGhldGlj"},{"type":"input_file","filename":"fixture.pdf","file_data":"data:application/pdf;base64,c3ludGhldGlj"}]}]}`
	t.Run("40 concurrent requests keep identities tools attachments and cache usage", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func(id int64) {
				defer wg.Done()
				resp, callErr := forward(ctx, "concurrent", id, body)
				if !assert.NoError(t, callErr) {
					return
				}
				defer resp.Body.Close()
				data, readErr := io.ReadAll(resp.Body)
				assert.NoError(t, readErr)
				assert.Equal(t, 200, resp.StatusCode)
				assert.Equal(t, completed, string(data), "SSE, tool calls, cache usage must remain byte-exact")
			}(42 + int64(i%2))
		}
		wg.Wait()
		require.Equal(t, int64(40), arrived.Load())
		require.Zero(t, runtime.inFlight.Load())
	})
	for _, tc := range []struct {
		name   string
		id     int64
		status int
	}{{"ordinary", 44, 200}, {"quota", 42, 429}, {"upstream_error", 43, 503}} {
		t.Run(tc.name, func(t *testing.T) {
			input := body
			if tc.name == "ordinary" {
				input = `{"stream":false,"store":true}`
			}
			resp, err := forward(context.Background(), tc.name, tc.id, input)
			require.NoError(t, err)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tc.status, resp.StatusCode)
			if tc.name == "quota" {
				require.Equal(t, "usage_limit_reached", gjson.GetBytes(data, "error.type").String())
			} else if tc.name == "upstream_error" {
				require.Equal(t, "original-upstream-error", string(data))
			}
			require.Zero(t, runtime.inFlight.Load())
		})
	}
	t.Run("multi turn coding browser search and client cache keys", func(t *testing.T) {
		for _, tool := range []string{"shell", "apply_patch", "browser_open", "browser_click", "browser_screenshot", "web_search"} {
			t.Run(tool, func(t *testing.T) {
				toolDef := map[string]any{"type": "function", "name": tool, "parameters": map[string]any{"type": "object"}}
				if tool == "web_search" {
					toolDef = map[string]any{"type": "web_search", "search_context_size": "high"}
				}
				input := []any{map[string]any{"role": "user", "content": "Use the requested tool and keep the conversation context."}}
				var previousKey string
				for turn := 0; turn < 2; turn++ {
					if turn == 1 {
						input = append(input,
							map[string]any{"type": "function_call", "name": "browser_open", "call_id": "call-browser", "arguments": `{"url":"https://example.invalid/fixture"}`},
							map[string]any{"type": "function_call_output", "call_id": "call-browser", "output": "synthetic page text, links and screenshot result"},
							map[string]any{"role": "user", "content": "Continue from that result."})
					}
					payload := map[string]any{"model": "fixture-model", "input": input, "tools": []any{toolDef},
						"instructions": "Fixture system instructions", "reasoning": map[string]any{"effort": "high"},
						"metadata": map[string]any{"test": tool}, "max_output_tokens": 200, "store": true, "stream": false}
					raw, err := json.Marshal(payload)
					require.NoError(t, err)
					resp, err := forward(context.Background(), "history", 42, string(raw))
					require.NoError(t, err)
					result, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.NoError(t, resp.Body.Close())
					require.Equal(t, completed, string(result))
					actual := <-observed
					for _, field := range []string{"model", "input", "tools", "instructions", "reasoning", "metadata", "max_output_tokens"} {
						require.JSONEq(t, gjson.GetBytes(raw, field).Raw, gjson.GetBytes(actual, field).Raw, field)
					}
					key := gjson.GetBytes(actual, "prompt_cache_key").String()
					require.NotEmpty(t, key)
					if previousKey != "" {
						require.Equal(t, previousKey, key, "continued conversation keeps its cache key")
					}
					previousKey = key
				}
			})
		}
	})
	for _, status := range []string{"400", "401", "403", "429", "500", "502", "503"} {
		t.Run("preserve unrelated upstream "+status, func(t *testing.T) {
			resp, err := forward(context.Background(), status, 42, body)
			require.NoError(t, err)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, status, fmt.Sprint(resp.StatusCode))
			require.Equal(t, `{"error":{"code":"fixture_error","message":"original error"}}`, string(data))
			require.Zero(t, runtime.inFlight.Load())
		})
	}
	t.Run("cancellation releases streaming request", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := forward(ctx, "cancel", 42, body)
		require.NoError(t, err)
		cancel()
		_ = resp.Body.Close()
		require.Eventually(t, func() bool { return runtime.inFlight.Load() == 0 }, time.Second, 10*time.Millisecond)
	})
	// Verify the runtime configuration can be reapplied after active requests.
	normalized, err := json.Marshal(map[string]int{"max_streams_per_connection": 4})
	require.NoError(t, err)
	require.NoError(t, runtime.validateAndApplyConfig(context.Background(), bytes.TrimSpace(normalized)))
}
