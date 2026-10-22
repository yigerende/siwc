package service

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

// siwcFlowTestClient 记录 SIWC 交换调用的参数，返回可配置的 token 响应。
type siwcFlowTestClient struct {
	exchangeCalls int
	lastCode      string
	lastVerifier  string
	lastRedirect  string
	lastClientID  string
	lastProxyURL  string
	tokenResp     *openai.TokenResponse
	exchangeErr   error

	refreshCalls        int
	lastRefreshToken    string
	lastRefreshClientID string
	refreshResp         *openai.TokenResponse
	refreshErr          error
}

func (c *siwcFlowTestClient) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {
	return nil, errors.New("codex exchange should not be used by siwc flow")
}

func (c *siwcFlowTestClient) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error) {
	return nil, errors.New("codex refresh should not be used by siwc flow")
}

func (c *siwcFlowTestClient) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string) (*openai.TokenResponse, error) {
	return nil, errors.New("codex refresh should not be used by siwc flow")
}

func (c *siwcFlowTestClient) ExchangeSIWCCode(ctx context.Context, code, codeVerifier, redirectURI, clientID, proxyURL string) (*openai.TokenResponse, error) {
	c.exchangeCalls++
	c.lastCode = code
	c.lastVerifier = codeVerifier
	c.lastRedirect = redirectURI
	c.lastClientID = clientID
	c.lastProxyURL = proxyURL
	if c.exchangeErr != nil {
		return nil, c.exchangeErr
	}
	return c.tokenResp, nil
}

func (c *siwcFlowTestClient) RefreshSIWCToken(ctx context.Context, refreshToken, clientID, proxyURL string) (*openai.TokenResponse, error) {
	c.refreshCalls++
	c.lastRefreshToken = refreshToken
	c.lastRefreshClientID = clientID
	if c.refreshErr != nil {
		return nil, c.refreshErr
	}
	return c.refreshResp, nil
}

func newSIWCFlowService(client *siwcFlowTestClient) *OpenAIOAuthService {
	svc := NewOpenAIOAuthService(nil, client)
	svc.siwcIDTokenVerifier = func(ctx context.Context, idToken, audience, nonce, proxyURL string) (*openai.SIWCIDTokenClaims, error) {
		if idToken != "valid-id-token" {
			return nil, errors.New("stub verification failed")
		}
		return &openai.SIWCIDTokenClaims{
			Sub:   "subject-1",
			Email: "user@example.com",
			Iss:   openai.SIWCIssuer,
			Aud:   []string{audience},
			Nonce: nonce,
		}, nil
	}
	return svc
}

func TestSIWCFlow_GenerateAuthURL_RegistrationParams(t *testing.T) {
	svc := newSIWCFlowService(&siwcFlowTestClient{})
	defer svc.Stop()

	result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{
		RedirectURI:    "http://127.0.0.1:1455/auth/callback",
		AgentNameHint:  "my-tool",
		ExtAgentHostID: "urn:uuid:11111111-1111-1111-1111-111111111111",
	})
	require.NoError(t, err)
	require.True(t, result.Registration)
	require.NotEmpty(t, result.SessionID)
	require.Equal(t, "urn:uuid:11111111-1111-1111-1111-111111111111", result.ExtAgentHostID)

	parsed, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	require.Equal(t, "auth.openai.com", parsed.Host)
	require.Equal(t, "/api/accounts/authorize", parsed.Path)
	q := parsed.Query()
	require.Equal(t, openai.SIWCRegistrationClientID, q.Get("client_id"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, openai.SIWCScopes, q.Get("scope"))
	require.Equal(t, openai.SIWCResource, q.Get("resource"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(t, q.Get("state"))
	require.NotEmpty(t, q.Get("nonce"))
	require.NotEmpty(t, q.Get("code_challenge"))
	require.Equal(t, "my-tool", q.Get("agent_name_hint"))
	require.Equal(t, "urn:uuid:11111111-1111-1111-1111-111111111111", q.Get("ext_agent_host_id"))
	require.Equal(t, "http://127.0.0.1:1455/auth/callback", q.Get("redirect_uri"))
	// 注册流程不携带 codex 简化流与提示参数。
	require.Empty(t, q.Get("id_token_hint"))
	require.Empty(t, q.Get("login_hint"))

	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	require.Equal(t, siwcFlowMarker, session.Flow)
	require.Equal(t, q.Get("state"), session.State)
	require.Equal(t, q.Get("nonce"), session.Nonce)
	require.Equal(t, openai.SIWCRegistrationClientID, session.ClientID)
	require.NotEmpty(t, session.CodeVerifier)
}

func TestSIWCFlow_GenerateAuthURL_ReauthorizationUsesIssuedClientID(t *testing.T) {
	svc := newSIWCFlowService(&siwcFlowTestClient{})
	defer svc.Stop()

	result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{
		RedirectURI:    "http://127.0.0.1:54321/auth/callback",
		ClientID:       "oaiapp_issued123",
		IDTokenHint:    "retained-id-token",
		LoginHint:      "user@example.com",
		ExtAgentHostID: "urn:uuid:11111111-1111-1111-1111-111111111111",
	})
	require.NoError(t, err)
	require.False(t, result.Registration)

	parsed, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	q := parsed.Query()
	require.Equal(t, "oaiapp_issued123", q.Get("client_id"))
	require.Equal(t, "retained-id-token", q.Get("id_token_hint"))
	require.Equal(t, "user@example.com", q.Get("login_hint"))
	// 复授权不携带注册提示。
	require.Empty(t, q.Get("agent_name_hint"))
	require.Empty(t, q.Get("ext_agent_host_id"))
}

func TestSIWCFlow_GenerateAuthURL_RejectsNonLoopbackRedirect(t *testing.T) {
	svc := newSIWCFlowService(&siwcFlowTestClient{})
	defer svc.Stop()

	for _, redirectURI := range []string{
		"http://localhost:1455/auth/callback",  // 规范要求 127.0.0.1
		"https://127.0.0.1:1455/auth/callback", // 必须 http
		"http://example.com/auth/callback",     // 非 loopback
		"http://127.0.0.1:1455",                // 路径必须稳定非空
	} {
		_, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{RedirectURI: redirectURI})
		require.Error(t, err, "redirect_uri %s should be rejected", redirectURI)
	}
}

func TestSIWCFlow_ExchangeCode_ValidationRules(t *testing.T) {
	t.Run("state mismatch rejected", func(t *testing.T) {
		svc := newSIWCFlowService(&siwcFlowTestClient{})
		defer svc.Stop()
		result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{})
		require.NoError(t, err)

		_, err = svc.ExchangeSIWCCode(context.Background(), &SIWCExchangeInput{
			SessionID:      result.SessionID,
			Code:           "c",
			State:          "wrong-state",
			IssuedClientID: "oaiapp_x",
		})
		require.ErrorContains(t, err, "OPENAI_SIWC_INVALID_STATE")
	})

	t.Run("registration requires issued client id", func(t *testing.T) {
		client := &siwcFlowTestClient{}
		svc := newSIWCFlowService(client)
		defer svc.Stop()
		result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{})
		require.NoError(t, err)

		session, ok := svc.sessionStore.Get(result.SessionID)
		require.True(t, ok)

		_, err = svc.ExchangeSIWCCode(context.Background(), &SIWCExchangeInput{
			SessionID: result.SessionID,
			Code:      "c",
			State:     session.State,
		})
		require.ErrorContains(t, err, "OPENAI_SIWC_ISSUED_CLIENT_ID_REQUIRED")
		require.Zero(t, client.exchangeCalls)
	})

	t.Run("client id mismatch rejected", func(t *testing.T) {
		client := &siwcFlowTestClient{}
		svc := newSIWCFlowService(client)
		defer svc.Stop()
		result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{ClientID: "oaiapp_saved"})
		require.NoError(t, err)

		session, ok := svc.sessionStore.Get(result.SessionID)
		require.True(t, ok)

		_, err = svc.ExchangeSIWCCode(context.Background(), &SIWCExchangeInput{
			SessionID:      result.SessionID,
			Code:           "c",
			State:          session.State,
			IssuedClientID: "oaiapp_other",
		})
		require.ErrorContains(t, err, "OPENAI_SIWC_CLIENT_ID_MISMATCH")
		require.Zero(t, client.exchangeCalls)
	})

	t.Run("invalid id token rejected", func(t *testing.T) {
		client := &siwcFlowTestClient{tokenResp: &openai.TokenResponse{
			AccessToken: "at",
			IDToken:     "bad-id-token",
			Scope:       openai.SIWCScopes,
		}}
		svc := newSIWCFlowService(client)
		defer svc.Stop()
		result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{})
		require.NoError(t, err)
		session, _ := svc.sessionStore.Get(result.SessionID)

		_, err = svc.ExchangeSIWCCode(context.Background(), &SIWCExchangeInput{
			SessionID:      result.SessionID,
			Code:           "auth-code",
			State:          session.State,
			IssuedClientID: "oaiapp_x",
		})
		require.ErrorContains(t, err, "OPENAI_SIWC_ID_TOKEN_INVALID")
	})
}

func TestSIWCFlow_ExchangeCode_SuccessBuildsSIWCTokenInfo(t *testing.T) {
	client := &siwcFlowTestClient{tokenResp: &openai.TokenResponse{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		IDToken:      "valid-id-token",
		ExpiresIn:    3600,
		Scope:        openai.SIWCScopes,
	}}
	svc := newSIWCFlowService(client)
	defer svc.Stop()

	result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{
		RedirectURI:    "http://127.0.0.1:1455/auth/callback",
		ExtAgentHostID: "urn:uuid:22222222-2222-2222-2222-222222222222",
	})
	require.NoError(t, err)
	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)

	tokenInfo, err := svc.ExchangeSIWCCode(context.Background(), &SIWCExchangeInput{
		SessionID:      result.SessionID,
		Code:           "auth-code",
		State:          session.State,
		IssuedClientID: "oaiapp_issued9",
	})
	require.NoError(t, err)
	require.Equal(t, "at-1", tokenInfo.AccessToken)
	require.Equal(t, "rt-1", tokenInfo.RefreshToken)
	require.Equal(t, "oaiapp_issued9", tokenInfo.ClientID)
	require.Equal(t, OpenAIAuthModeSIWC, tokenInfo.AuthMode)
	require.True(t, tokenInfo.PlanUseEnabled)
	require.Equal(t, "user@example.com", tokenInfo.Email)
	require.Equal(t, "subject-1", tokenInfo.SIWCSubject)
	require.Equal(t, "urn:uuid:22222222-2222-2222-2222-222222222222", tokenInfo.ExtAgentHostID)
	require.Contains(t, tokenInfo.GrantedScopes, openai.SIWCPlanUseScope)

	// 交换使用会话中的 PKCE verifier 与 redirect_uri，client_id 用签发值。
	require.Equal(t, 1, client.exchangeCalls)
	require.Equal(t, "auth-code", client.lastCode)
	require.Equal(t, session.CodeVerifier, client.lastVerifier)
	require.Equal(t, "http://127.0.0.1:1455/auth/callback", client.lastRedirect)
	require.Equal(t, "oaiapp_issued9", client.lastClientID)

	// 会话在成功交换后被消费，重放被拒。
	_, err = svc.ExchangeSIWCCode(context.Background(), &SIWCExchangeInput{
		SessionID:      result.SessionID,
		Code:           "auth-code",
		State:          session.State,
		IssuedClientID: "oaiapp_issued9",
	})
	require.ErrorContains(t, err, "OPENAI_SIWC_SESSION_NOT_FOUND")
}

func TestSIWCFlow_PlanUseDisabledWhenScopeMissing(t *testing.T) {
	client := &siwcFlowTestClient{tokenResp: &openai.TokenResponse{
		AccessToken: "at-1",
		IDToken:     "valid-id-token",
		ExpiresIn:   3600,
		Scope:       "openid profile email offline_access resource.invoke",
	}}
	svc := newSIWCFlowService(client)
	defer svc.Stop()

	result, err := svc.GenerateSIWCAuthURL(context.Background(), &SIWCAuthURLInput{})
	require.NoError(t, err)
	session, _ := svc.sessionStore.Get(result.SessionID)

	tokenInfo, err := svc.ExchangeSIWCCode(context.Background(), &SIWCExchangeInput{
		SessionID:      result.SessionID,
		Code:           "c",
		State:          session.State,
		IssuedClientID: "oaiapp_issued9",
	})
	require.NoError(t, err)
	require.False(t, tokenInfo.PlanUseEnabled)
}

func TestSIWCFlow_BuildAccountCredentialsMarksSIWCMode(t *testing.T) {
	svc := newSIWCFlowService(&siwcFlowTestClient{})
	defer svc.Stop()

	creds := svc.BuildAccountCredentials(&OpenAITokenInfo{
		AccessToken:    "at",
		RefreshToken:   "rt",
		ClientID:       "oaiapp_issued9",
		AuthMode:       OpenAIAuthModeSIWC,
		ExtAgentHostID: "urn:uuid:h1",
		GrantedScopes:  []string{openai.SIWCPlanUseScope, "openid"},
		PlanUseEnabled: true,
		SIWCSubject:    "subject-1",
		ExpiresAt:      1790000000,
	})

	require.Equal(t, OpenAIAuthModeSIWC, creds[openAIAuthModeCredentialKey])
	require.Equal(t, OpenAIAuthModeSIWC, creds[openAIAuthModeLegacyCredentialKey])
	require.Equal(t, "oaiapp_issued9", creds["client_id"])
	require.Equal(t, "urn:uuid:h1", creds["ext_agent_host_id"])
	require.Equal(t, "subject-1", creds["subject"])
	require.Equal(t, true, creds["plan_use_enabled"])
	require.Equal(t, "Bearer", creds["token_type"])
	scopes, ok := creds["granted_scopes"].([]string)
	require.True(t, ok)
	require.Contains(t, scopes, openai.SIWCPlanUseScope)

	account := &Account{Credentials: creds, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.True(t, account.IsOpenAISIWC())
	require.Equal(t, "oaiapp_issued9", account.GetOpenAISIWCClientID())
	require.Equal(t, "urn:uuid:h1", account.GetOpenAISIWCHostID())
}

func TestSIWCFlow_RefreshAccountToken_UsesSIWCEndpoint(t *testing.T) {
	client := &siwcFlowTestClient{refreshResp: &openai.TokenResponse{
		AccessToken:  "at-2",
		RefreshToken: "rt-2",
		IDToken:      "valid-id-token",
		ExpiresIn:    3600,
		Scope:        openai.SIWCScopes,
	}}
	svc := newSIWCFlowService(client)
	defer svc.Stop()

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"client_id":     "oaiapp_issued9",
			"auth_mode":     OpenAIAuthModeSIWC,
			"expires_at":    "2000-01-01T00:00:00Z",
		},
	}

	tokenInfo, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "at-2", tokenInfo.AccessToken)
	require.Equal(t, "rt-2", tokenInfo.RefreshToken)
	require.Equal(t, OpenAIAuthModeSIWC, tokenInfo.AuthMode)
	require.Equal(t, 1, client.refreshCalls)
	require.Equal(t, "rt-1", client.lastRefreshToken)
	require.Equal(t, "oaiapp_issued9", client.lastRefreshClientID)
	// Codex 刷新路径不应被触碰。
	require.Zero(t, client.exchangeCalls)
}
