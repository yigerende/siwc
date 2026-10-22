package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pluginRoutingHTTPUpstream struct {
	doCalls        int
	doWithTLSCalls int
}

func (u *pluginRoutingHTTPUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.doCalls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("legacy")),
	}, nil
}

func (u *pluginRoutingHTTPUpstream) DoWithTLS(
	request *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	u.doWithTLSCalls++
	return u.Do(request, proxyURL, accountID, accountConcurrency)
}

func TestPluginManagerRoutingDoesNotTouchAPIKeyOrOtherProviders(t *testing.T) {
	manager := &PluginManager{}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1/responses", nil)
	require.NoError(t, err)

	accounts := []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{ID: 3, Platform: PlatformGemini, Type: AccountTypeOAuth},
	}
	for _, account := range accounts {
		response, handled, routeErr := manager.RoundTripOpenAIOAuth(context.Background(), request, "", account)
		assert.Nil(t, response)
		assert.False(t, handled)
		assert.NoError(t, routeErr)
	}
}

func TestPluginManagerRoutingKeepsOAuthOnLegacyPathWithoutEnabledBinding(t *testing.T) {
	manager := &PluginManager{}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1/responses", nil)
	require.NoError(t, err)
	account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	response, handled, routeErr := manager.RoundTripOpenAIOAuth(context.Background(), request, "", account)

	assert.Nil(t, response)
	assert.False(t, handled)
	assert.NoError(t, routeErr)
}

func TestPluginManagerRoutingSelectsOnlyEligibleOpenAIOAuthAccounts(t *testing.T) {
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "测试不可用"})

	assert.True(t, manager.ShouldRouteOpenAIOAuth(&Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	assert.False(t, manager.ShouldRouteOpenAIOAuth(&Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}))
	assert.False(t, manager.ShouldRouteOpenAIOAuth(&Account{ID: 10, Platform: PlatformGrok, Type: AccountTypeOAuth}))
	assert.False(t, manager.ShouldRouteOpenAIOAuth(nil))
}

func TestOpenAIGatewayPluginRoutingPreservesAPIKeyAndFailsClosedForOAuth(t *testing.T) {
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "测试不可用"})
	upstream := &pluginRoutingHTTPUpstream{}
	service := &OpenAIGatewayService{pluginManager: manager, httpUpstream: upstream}

	apiKeyRequest, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1/responses", nil)
	require.NoError(t, err)
	apiKeyResponse, err := service.doOpenAIUpstream(apiKeyRequest, "", &Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
	})
	require.NoError(t, err)
	require.NotNil(t, apiKeyResponse)
	_ = apiKeyResponse.Body.Close()
	assert.Equal(t, 1, upstream.doCalls)

	oauthRequest, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1/responses", nil)
	require.NoError(t, err)
	oauthResponse, err := service.doOpenAIUpstream(oauthRequest, "", &Account{
		ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
	})
	require.Error(t, err)
	assert.Nil(t, oauthResponse)
	assert.Contains(t, err.Error(), "插件不可用")
	assert.Equal(t, 1, upstream.doCalls)
}

func TestStablePluginBucketIsDeterministicAndBounded(t *testing.T) {
	for id := int64(1); id <= 1000; id++ {
		first := stablePluginBucket(id)
		assert.Equal(t, first, stablePluginBucket(id))
		assert.Less(t, first, uint64(100))
	}
}

func TestPluginManagerRoutingSIWCFailsFastWhenPluginUnavailable(t *testing.T) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	siwcAccount := &Account{
		ID:          77,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_mode": OpenAIAuthModeSIWC},
	}

	t.Run("route nil: handled error, never falls back to legacy path", func(t *testing.T) {
		manager := &PluginManager{}
		response, handled, routeErr := manager.RoundTripOpenAIOAuth(context.Background(), request, "", siwcAccount)
		assert.Nil(t, response)
		assert.True(t, handled, "SIWC 账号在插件不可用时必须拦截而不是回落默认出口")
		require.Error(t, routeErr)
		assert.Contains(t, routeErr.Error(), "SIWC")
	})

	t.Run("route present but runtime nil: handled error", func(t *testing.T) {
		manager := &PluginManager{}
		manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "正在启动"})
		response, handled, routeErr := manager.RoundTripOpenAIOAuth(context.Background(), request, "", siwcAccount)
		assert.Nil(t, response)
		assert.True(t, handled)
		require.Error(t, routeErr)
	})

	t.Run("non-SIWC OAuth keeps legacy fallback when route nil", func(t *testing.T) {
		manager := &PluginManager{}
		account := &Account{ID: 78, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		response, handled, routeErr := manager.RoundTripOpenAIOAuth(context.Background(), request, "", account)
		assert.Nil(t, response)
		assert.False(t, handled, "非 SIWC 账号保持原有回落行为")
		assert.NoError(t, routeErr)
	})
}

type statusStubRepository struct {
	PluginRepository
}

func (r *statusStubRepository) GetByID(context.Context, int64) (*PluginInstallation, error) {
	return &PluginInstallation{ID: 7}, nil
}

// Status is the read-only, ungated runtime-status channel: when the plugin is not
// running it must report "not running" with no status blob and never error or start
// a runtime (that side-effecting behaviour belongs to Test, not Status).
func TestPluginManagerStatusReportsNotRunningWithoutRuntime(t *testing.T) {
	manager := &PluginManager{repo: &statusStubRepository{}}
	resp, err := manager.Status(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.False(t, resp.Healthy)
	assert.Equal(t, "插件未运行", resp.Message)
	assert.Empty(t, resp.StatusJson)
}
