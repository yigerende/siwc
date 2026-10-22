package service

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

// SIWC (Sign in with ChatGPT) token-sharing 授权流程。
//
// 与 Codex CLI OAuth（/oauth/authorize + 固定 client_id）不同，SIWC 使用
// auth.openai.com/api/accounts/authorize 动态注册（dynamic_agent_client），
// 回调返回签发的 client_id；token 交换与刷新都走
// auth.openai.com/api/accounts/oauth/token 并携带 resource 参数。
//
// 预期调用方是部署方自己的「OAuth 接口程序」：
//  1. POST /api/v1/admin/openai/siwc/generate-auth-url 拿授权 URL 与 session_id；
//  2. 本地起 127.0.0.1 回调监听并用系统浏览器打开授权 URL；
//  3. 回调拿到 code/state/（首次注册的）签发 client_id；
//  4. POST /api/v1/admin/openai/siwc/exchange-code 换 token（服务端完成
//     PKCE 交换 + ID token JWKS 验签 + scope 检查）；
//  5. POST /api/v1/admin/openai/create-from-oauth（带 siwc 会话）建号，
//     或直接 POST /api/v1/admin/openai/siwc/create-from-oauth 一步完成。

const siwcFlowMarker = "siwc"

// SIWCAuthURLInput 生成 SIWC 授权 URL 的输入。
type SIWCAuthURLInput struct {
	ProxyID *int64
	// RedirectURI 必须是 http://127.0.0.1:<port>/auth/callback 形式的
	// loopback 回调（端口可变、路径固定；不要用 localhost）。为空时使用默认值。
	RedirectURI string
	// AgentNameHint 应用名提示，仅首次注册使用；默认 "sub2api"。
	AgentNameHint string
	// ClientID 为空表示首次注册（dynamic_agent_client）；复授权传保存的签发 client_id。
	ClientID string
	// IDTokenHint / LoginHint 复授权可选提示。
	IDTokenHint string
	LoginHint   string
	// ExtAgentHostID 宿主稳定标识（urn:uuid:...）。为空时首次注册将生成并返回，
	// 调用方必须持久化并在后续授权中回传。
	ExtAgentHostID string
}

// SIWCAuthURLResult 生成 SIWC 授权 URL 的结果。
type SIWCAuthURLResult struct {
	AuthURL        string `json:"auth_url"`
	SessionID      string `json:"session_id"`
	ExtAgentHostID string `json:"ext_agent_host_id"`
	// Registration 报告本次是否为首次动态注册。
	Registration bool `json:"registration"`
}

// GenerateSIWCAuthURL 生成 SIWC 授权 URL 并登记会话状态（PKCE/state/nonce）。
func (s *OpenAIOAuthService) GenerateSIWCAuthURL(ctx context.Context, input *SIWCAuthURLInput) (*SIWCAuthURLResult, error) {
	if input == nil {
		input = &SIWCAuthURLInput{}
	}

	state, err := openai.GenerateState()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_SIWC_STATE_FAILED", "failed to generate state: %v", err)
	}
	codeVerifier, err := openai.GenerateCodeVerifier()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_SIWC_VERIFIER_FAILED", "failed to generate code verifier: %v", err)
	}
	codeChallenge := openai.GenerateCodeChallenge(codeVerifier)
	nonce, err := openai.GenerateState()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_SIWC_NONCE_FAILED", "failed to generate nonce: %v", err)
	}
	sessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_SIWC_SESSION_FAILED", "failed to generate session ID: %v", err)
	}

	proxyURL := ""
	if input.ProxyID != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *input.ProxyID)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadRequest, "OPENAI_SIWC_PROXY_NOT_FOUND", "proxy not found: %v", err)
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	redirectURI := strings.TrimSpace(input.RedirectURI)
	if redirectURI == "" {
		redirectURI = openai.SIWCDefaultRedirectURI
	}
	if err := validateSIWCRedirectURI(redirectURI); err != nil {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_INVALID_REDIRECT_URI", err.Error())
	}

	clientID := strings.TrimSpace(input.ClientID)
	registration := clientID == "" || clientID == openai.SIWCRegistrationClientID
	if registration {
		clientID = openai.SIWCRegistrationClientID
	}

	extAgentHostID := strings.TrimSpace(input.ExtAgentHostID)
	if extAgentHostID == "" {
		extAgentHostID = "urn:uuid:" + uuid.NewString()
	}

	agentNameHint := strings.TrimSpace(input.AgentNameHint)
	if agentNameHint == "" {
		agentNameHint = "sub2api"
	}

	authURL := openai.BuildSIWCAuthorizationURL(clientID, state, codeChallenge, redirectURI, openai.SIWCAuthorizeOptions{
		AgentNameHint:  agentNameHint,
		ExtAgentHostID: extAgentHostID,
		IDTokenHint:    input.IDTokenHint,
		LoginHint:      input.LoginHint,
		Nonce:          nonce,
	})

	s.sessionStore.Set(sessionID, &openai.OAuthSession{
		State:          state,
		CodeVerifier:   codeVerifier,
		ClientID:       clientID,
		RedirectURI:    redirectURI,
		ProxyURL:       proxyURL,
		CreatedAt:      time.Now(),
		Flow:           siwcFlowMarker,
		Nonce:          nonce,
		ExtAgentHostID: extAgentHostID,
	})

	return &SIWCAuthURLResult{
		AuthURL:        authURL,
		SessionID:      sessionID,
		ExtAgentHostID: extAgentHostID,
		Registration:   registration,
	}, nil
}

// SIWCExchangeInput SIWC 授权码交换的输入。
type SIWCExchangeInput struct {
	SessionID string
	Code      string
	State     string
	// IssuedClientID 是回调返回的签发 client_id。首次注册（会话使用
	// dynamic_agent_client）时必填；复授权时可选，提供且与会话不一致则拒绝。
	IssuedClientID string
	RedirectURI    string
	ProxyID        *int64
}

// ExchangeSIWCCode 校验回调参数并与 SIWC token 端点交换授权码。
// 成功路径完成：state 常量时间校验 → 签发 client_id 规则校验 → PKCE 交换 →
// ID token JWKS 验签（iss/aud/exp/nonce）→ granted scope 检查。
func (s *OpenAIOAuthService) ExchangeSIWCCode(ctx context.Context, input *SIWCExchangeInput) (*OpenAITokenInfo, error) {
	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_SESSION_NOT_FOUND", "session not found or expired")
	}
	if session.Flow != siwcFlowMarker {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_SESSION_FLOW_MISMATCH", "session was not started as a siwc flow")
	}
	if input.State == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_STATE_REQUIRED", "oauth state is required")
	}
	if subtle.ConstantTimeCompare([]byte(input.State), []byte(session.State)) != 1 {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_INVALID_STATE", "invalid oauth state")
	}

	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *input.ProxyID)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadRequest, "OPENAI_SIWC_PROXY_NOT_FOUND", "proxy not found: %v", err)
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	redirectURI := session.RedirectURI
	if strings.TrimSpace(input.RedirectURI) != "" {
		redirectURI = strings.TrimSpace(input.RedirectURI)
	}

	// 签发 client_id 规则（SIWC sign-in 文档第 3 步）：
	// 注册会话必须由回调带回签发 client_id；复授权会话保留原 client_id，
	// 回调带回不同 client_id 时拒绝而不是替换账号注册。
	issuedClientID := strings.TrimSpace(input.IssuedClientID)
	exchangeClientID := issuedClientID
	if session.ClientID == openai.SIWCRegistrationClientID {
		if issuedClientID == "" || issuedClientID == openai.SIWCRegistrationClientID {
			return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_ISSUED_CLIENT_ID_REQUIRED", "registration callback must supply the issued client_id")
		}
	} else if issuedClientID != "" && issuedClientID != session.ClientID {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_CLIENT_ID_MISMATCH", "callback client_id does not match the selected account registration")
	} else if issuedClientID == "" {
		exchangeClientID = session.ClientID
	}

	tokenResp, err := s.oauthClient.ExchangeSIWCCode(ctx, input.Code, session.CodeVerifier, redirectURI, exchangeClientID, proxyURL)
	if err != nil {
		return nil, err
	}

	// ID token 验签 + 声明校验（iss/aud/exp/nonce）。
	var claims *openai.SIWCIDTokenClaims
	if tokenResp.IDToken != "" {
		claims, err = s.siwcIDTokenVerifier(ctx, tokenResp.IDToken, exchangeClientID, session.Nonce, proxyURL)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadRequest, "OPENAI_SIWC_ID_TOKEN_INVALID", "id token validation failed: %v", err)
		}
	} else {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_ID_TOKEN_MISSING", "siwc token response has no id_token")
	}

	// granted scope 检查：没有 chatgpt.tokens.use.direct 时保留登录态但标记计划用量未启用。
	granted := strings.Fields(tokenResp.Scope)
	planUseEnabled := false
	for _, scope := range granted {
		if scope == openai.SIWCPlanUseScope {
			planUseEnabled = true
			break
		}
	}

	// 交换成功即消费会话，防止 code/state 重放。
	s.sessionStore.Delete(input.SessionID)

	tokenInfo := &OpenAITokenInfo{
		AccessToken:    tokenResp.AccessToken,
		RefreshToken:   tokenResp.RefreshToken,
		IDToken:        tokenResp.IDToken,
		ExpiresIn:      int64(tokenResp.ExpiresIn),
		ExpiresAt:      time.Now().Unix() + int64(tokenResp.ExpiresIn),
		ClientID:       exchangeClientID,
		AuthMode:       OpenAIAuthModeSIWC,
		Email:          claims.Email,
		GrantedScopes:  granted,
		PlanUseEnabled: planUseEnabled,
		ExtAgentHostID: session.ExtAgentHostID,
		SIWCSubject:    claims.Sub,
	}
	return tokenInfo, nil
}

// RefreshSIWCAccountToken 刷新 SIWC 账号 token（供 RefreshAccountToken 分支调用）。
// 复用账号保存的签发 client_id 与宿主标识；刷新端点带 resource、省略 scope。
func (s *OpenAIOAuthService) RefreshSIWCAccountToken(ctx context.Context, account *Account) (*OpenAITokenInfo, error) {
	var proxyURL string
	if account.ProxyID != nil && s.proxyRepo != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
		if err == nil && proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	refreshToken := strings.TrimSpace(account.GetCredential("refresh_token"))
	if refreshToken == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_NO_REFRESH_TOKEN", "siwc account has no refresh token")
	}
	clientID := account.GetOpenAISIWCClientID()
	if clientID == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_SIWC_NO_CLIENT_ID", "siwc account has no issued client_id saved")
	}

	tokenResp, err := s.oauthClient.RefreshSIWCToken(ctx, refreshToken, clientID, proxyURL)
	if err != nil {
		return nil, err
	}

	tokenInfo := &OpenAITokenInfo{
		AccessToken:    tokenResp.AccessToken,
		RefreshToken:   tokenResp.RefreshToken,
		IDToken:        tokenResp.IDToken,
		ExpiresIn:      int64(tokenResp.ExpiresIn),
		ExpiresAt:      time.Now().Unix() + int64(tokenResp.ExpiresIn),
		ClientID:       clientID,
		AuthMode:       OpenAIAuthModeSIWC,
		GrantedScopes:  strings.Fields(tokenResp.Scope),
		PlanUseEnabled: scopeListContains(tokenResp.Scope, openai.SIWCPlanUseScope),
		ExtAgentHostID: account.GetOpenAISIWCHostID(),
	}

	// 刷新返回的 id_token 做尽力校验（跳过 nonce：刷新流程没有新的 nonce），
	// 失败不阻断刷新——access token 本身就是端点刚签发的。
	if tokenResp.IDToken != "" {
		if claims, err := s.siwcIDTokenVerifier(ctx, tokenResp.IDToken, clientID, "", proxyURL); err != nil {
			slog.Warn("openai_siwc_refresh_id_token_verify_failed", "account_id", account.ID, "error", err)
		} else if tokenInfo.Email == "" {
			tokenInfo.Email = claims.Email
		}
	}
	return tokenInfo, nil
}

func scopeListContains(scopeString string, want string) bool {
	for _, scope := range strings.Fields(scopeString) {
		if scope == want {
			return true
		}
	}
	return false
}

// validateSIWCRedirectURI 强制 loopback 回调规范：http + 127.0.0.1 主机 +
// 固定路径段。端口允许变化；localhost 会被 auth 服务拒绝。
func validateSIWCRedirectURI(redirectURI string) error {
	parsed, err := url.Parse(strings.TrimSpace(redirectURI))
	if err != nil {
		return fmt.Errorf("redirect_uri is not a valid URL: %v", err)
	}
	if parsed.Scheme != "http" {
		return fmt.Errorf("siwc redirect_uri must use http loopback (got scheme %q)", parsed.Scheme)
	}
	if strings.ToLower(parsed.Hostname()) != "127.0.0.1" {
		return fmt.Errorf("siwc redirect_uri must use host 127.0.0.1, not localhost or other hosts (got %q)", parsed.Hostname())
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("siwc redirect_uri must keep a stable path (got %q)", parsed.Path)
	}
	return nil
}
