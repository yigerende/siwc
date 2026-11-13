package service

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func configurationUpdateTestGateway(t *testing.T, enabled bool) (*OpenAIGatewayService, *Account) {
	t.Helper()
	s := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	require.NoError(t, s.SetSIWCConfigurationUpdateCompatibility(context.Background(), enabled))
	return &OpenAIGatewayService{settingService: s}, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc", "chatgpt_account_id": "test"}}
}

func TestSIWCConfigurationUpdateCompatibility(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"model":"gpt-5.5","reasoning":{"effort":"high","summary":"auto"},"input":[{"type":"additional_tools","tools":[]},{"type":"message","role":"user","content":"hello configuration_update"},{"type":"configuration_update","reasoning":{"effort":"none"}},{"type":"function_call","call_id":"a","arguments":"{}","name":"run"},{"type":"function_call_output","call_id":"a","output":"ok"},{"type":"configuration_update","reasoning":{"effort":"low"}}],"tools":[{"type":"function","name":"run"}],"large_number":9007199254740993}`)
	s, account := configurationUpdateTestGateway(t, false)
	require.Equal(t, body, s.normalizeSIWCConfigurationUpdates(ctx, account, body), "off must preserve exact bytes")
	require.NoError(t, s.settingService.SetSIWCConfigurationUpdateCompatibility(ctx, true))
	result := s.normalizeSIWCConfigurationUpdates(ctx, account, body)
	require.Len(t, gjson.GetBytes(result, "input").Array(), 4)
	require.Equal(t, "high", gjson.GetBytes(result, "reasoning.effort").String(), "group mapping wins")
	require.Equal(t, "auto", gjson.GetBytes(result, "reasoning.summary").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(result, "large_number").Raw)
	require.Equal(t, gjson.GetBytes(body, "tools").Raw, gjson.GetBytes(result, "tools").Raw)
	for i, source := range []int{0, 1, 3, 4} {
		require.JSONEq(t, gjson.GetBytes(body, "input").Array()[source].Raw, gjson.GetBytes(result, "input").Array()[i].Raw)
	}
	account.Credentials["auth_mode"] = "oauth"
	require.Equal(t, body, s.normalizeSIWCConfigurationUpdates(ctx, account, body))
	account.Type = AccountTypeAPIKey
	account.Credentials["auth_mode"] = "siwc"
	require.Equal(t, body, s.normalizeSIWCConfigurationUpdates(ctx, account, body))
}

func TestSIWCConfigurationUpdateEffortFallback(t *testing.T) {
	s, a := configurationUpdateTestGateway(t, true)
	for _, tc := range []struct {
		name, body, effort string
		size               int
	}{
		{"last nonempty", `{"input":[{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"configuration_update","reasoning":{"effort":"none"}},{"type":"configuration_update","reasoning":{}}]}`, "none", 0},
		{"mapped none unchanged", `{"reasoning":{"effort":"none"},"input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`, "none", 0},
		{"no effort", `{"input":[{"type":"configuration_update","additional_tools":[]} ,{"type":"message","role":"user","content":"hi"}]}`, "", 1},
		{"escaped type", `{"input":[{"type":"configuration_\u0075pdate","reasoning":{"effort":"low"}}]}`, "low", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := s.normalizeSIWCConfigurationUpdates(context.Background(), a, []byte(tc.body))
			require.True(t, gjson.ValidBytes(result))
			require.Equal(t, tc.effort, gjson.GetBytes(result, "reasoning.effort").String())
			require.Len(t, gjson.GetBytes(result, "input").Array(), tc.size)
		})
	}
	for _, body := range []string{`{"input":"hello"}`, `{"input":[{"type":"message","content":"configuration_update"}]}`, `{"input":[]}`} {
		require.Equal(t, []byte(body), s.normalizeSIWCConfigurationUpdates(context.Background(), a, []byte(body)))
	}
}

func TestSIWCConfigurationUpdateRequestBuilders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, enabled := range []bool{false, true} {
		for _, passthrough := range []bool{false, true} {
			s, a := configurationUpdateTestGateway(t, enabled)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			body := []byte(`{"model":"gpt-5.5","reasoning":{"effort":"high"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}},{"type":"message","role":"user","content":"hello"}]}`)
			req, err := s.buildUpstreamRequest(context.Background(), c, a, body, "test", true, "", false)
			if passthrough {
				req, err = s.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, a, body, "test")
			}
			require.NoError(t, err)
			out, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			if enabled {
				require.Len(t, gjson.GetBytes(out, "input").Array(), 1)
			} else {
				require.Equal(t, body, out)
			}
			require.Equal(t, "high", gjson.GetBytes(out, "reasoning.effort").String())
			require.Equal(t, int64(len(out)), req.ContentLength)
			replay, err := req.GetBody()
			require.NoError(t, err)
			replayed, err := io.ReadAll(replay)
			require.NoError(t, err)
			require.Equal(t, out, replayed)
			replay.Close()
		}
	}
}

type configurationUpdateFailingRepo struct {
	*panelRateLimitSettingRepo
	fail bool
}

func (r *configurationUpdateFailingRepo) Set(ctx context.Context, key, value string) error {
	if r.fail {
		return errors.New("write unavailable")
	}
	return r.panelRateLimitSettingRepo.Set(ctx, key, value)
}

func TestSIWCConfigurationUpdateSettings(t *testing.T) {
	ctx := context.Background()
	r := &configurationUpdateFailingRepo{panelRateLimitSettingRepo: &panelRateLimitSettingRepo{}}
	s := newPanelRateLimitTestService(r)
	enabled, err := s.GetSIWCConfigurationUpdateCompatibility(ctx)
	require.NoError(t, err)
	require.False(t, enabled)
	require.NoError(t, s.SetSIWCConfigurationUpdateCompatibility(ctx, true))
	for i := 0; i < 100; i++ {
		enabled, err = s.GetSIWCConfigurationUpdateCompatibility(ctx)
		require.NoError(t, err)
		require.True(t, enabled)
	}
	require.Equal(t, 1, r.getValueCalls, "cached requests must not read DB")
	enabled, err = newPanelRateLimitTestService(r).GetSIWCConfigurationUpdateCompatibility(ctx)
	require.NoError(t, err)
	require.True(t, enabled, "persist across restart")
	r.fail = true
	require.Error(t, s.SetSIWCConfigurationUpdateCompatibility(ctx, false))
	enabled, err = s.GetSIWCConfigurationUpdateCompatibility(ctx)
	require.NoError(t, err)
	require.True(t, enabled, "failed save must not alter cache")
	r.getValueErr = errors.New("read unavailable")
	s.siwcConfigurationUpdateCache.Store(&cachedSIWCConfigurationUpdate{enabled: true, expires: time.Now().Add(-time.Second)})
	enabled, err = s.GetSIWCConfigurationUpdateCompatibility(ctx)
	require.Error(t, err)
	require.True(t, enabled, "retain last good setting on transient DB failure")
	r.fail = false
	require.NoError(t, s.SetSIWCConfigurationUpdateCompatibility(ctx, false))
	enabled, err = s.GetSIWCConfigurationUpdateCompatibility(ctx)
	require.NoError(t, err)
	require.False(t, enabled)
}

func TestSIWCConfigurationUpdateConcurrentReadSave(t *testing.T) {
	s := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = s.SetSIWCConfigurationUpdateCompatibility(context.Background(), i%4 == 0)
			} else {
				_, _ = s.GetSIWCConfigurationUpdateCompatibility(context.Background())
			}
		}(i)
	}
	wg.Wait()
	require.NoError(t, s.SetSIWCConfigurationUpdateCompatibility(context.Background(), true))
	got, err := s.GetSIWCConfigurationUpdateCompatibility(context.Background())
	require.NoError(t, err)
	require.True(t, got)
	persisted, err := newPanelRateLimitTestService(s.settingRepo).GetSIWCConfigurationUpdateCompatibility(context.Background())
	require.NoError(t, err)
	require.Equal(t, got, persisted)
}
