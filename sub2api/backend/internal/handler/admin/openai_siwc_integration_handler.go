package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type siwcIntegrationGenerateRequest struct {
	Email    string `json:"email" binding:"required,email"`
	ProxyURL string `json:"proxy_url"`
	HostID   string `json:"ext_agent_host_id" binding:"required"`
}

func (h *OpenAIOAuthHandler) GenerateSIWCIntegration(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req siwcIntegrationGenerateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid integration request")
		return
	}
	result, err := h.openaiOAuthService.GenerateSIWCIntegration(c.Request.Context(), adminActorScope(c), req.Email, &service.SIWCAuthURLInput{
		ProxyURL: req.ProxyURL, ExtAgentHostID: req.HostID, RedirectURI: "http://127.0.0.1:1456/auth/callback", AgentNameHint: "sub2api",
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type siwcIntegrationCallbackRequest struct {
	SessionID      string   `json:"session_id" binding:"required"`
	CallbackURL    string   `json:"callback_url" binding:"required"`
	Email          string   `json:"email" binding:"required,email"`
	WebAccessToken string   `json:"web_access_token" binding:"required"`
	WebAccountID   string   `json:"web_account_id" binding:"required"`
	Name           string   `json:"name"`
	GroupIDs       []int64  `json:"group_ids" binding:"required,min=1,dive,gt=0"`
	ProxyID        *int64   `json:"proxy_id"`
	Concurrency    int      `json:"concurrency" binding:"required,min=1,max=100"`
	Priority       int      `json:"priority" binding:"required,min=1,max=100"`
	Models         []string `json:"models"`
	CpaWS          bool     `json:"cpa_ws"`
}

// ImportSIWCIntegration exchanges and validates the callback, then imports into
// the caller-selected groups. Only an account ID enters the idempotency ledger;
// tokens are read from the existing credential store for both first/replay replies.
func (h *OpenAIOAuthHandler) ImportSIWCIntegration(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var req siwcIntegrationCallbackRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid integration callback")
		return
	}
	key, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil || key == "" {
		response.ErrorFrom(c, service.ErrIdempotencyKeyRequired)
		return
	}
	if service.DefaultIdempotencyCoordinator() == nil {
		response.ErrorFrom(c, service.ErrIdempotencyStoreUnavail)
		return
	}
	owner := adminActorScope(c)
	fingerprint, err := service.BuildIdempotencyFingerprint("POST", c.FullPath(), owner, req)
	if err != nil {
		response.BadRequest(c, "Invalid integration request")
		return
	}
	result, err := executeAdminIdempotentWithTimeout(c, "admin.siwc.integration.import", req, service.DefaultWriteIdempotencyTTL(), 150*time.Second, func(ctx context.Context) (any, error) {
		// Validate selected groups before consuming the one-use OAuth code.
		for _, id := range req.GroupIDs {
			group, e := h.adminService.GetGroup(ctx, id)
			if e != nil || group == nil || group.Platform != "openai" {
				return nil, infraerrors.BadRequest("SIWC_GROUP_INVALID", "select existing OpenAI groups")
			}
		}
		if req.ProxyID != nil {
			proxy, e := h.adminService.GetProxy(ctx, *req.ProxyID)
			if e != nil || proxy == nil {
				return nil, infraerrors.BadRequest("SIWC_PROXY_INVALID", "selected account proxy does not exist")
			}
		}
		if err := h.adminService.ValidateAccountGroupBindings(ctx, req.GroupIDs); err != nil {
			return nil, err
		}
		id, err := h.openaiOAuthService.ImportSIWCIntegration(ctx, owner, req.SessionID, req.CallbackURL, fingerprint, func(tokens *service.OpenAITokenInfo) (int64, error) {
			if !strings.EqualFold(strings.TrimSpace(req.Email), tokens.Email) {
				return 0, infraerrors.BadRequest("SIWC_EMAIL_MISMATCH", "account email mismatch")
			}
			credentials := h.openaiOAuthService.BuildAccountCredentials(tokens)
			credentials["subject"] = tokens.SIWCSubject
			credentials["siwc_web_access_token"], credentials["siwc_web_account_id"], credentials["siwc_app_id"] = req.WebAccessToken, req.WebAccountID, tokens.ClientID
			if len(req.Models) > 0 {
				mapping := map[string]string{}
				for _, name := range req.Models {
					if name = strings.TrimSpace(name); name != "" {
						mapping[name] = name
					}
				}
				credentials["model_mapping"] = mapping
			}
			name := strings.TrimSpace(req.Name)
			if name == "" {
				name = tokens.Email
			}
			account, err := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
				Name: name, Platform: "openai", Type: "oauth", Credentials: credentials,
				Extra:    map[string]any{"auth_provider": "siwc", "import_source": "siwc", "codex_fingerprint_mode": "off"},
				GroupIDs: req.GroupIDs, ProxyID: req.ProxyID, Concurrency: req.Concurrency, Priority: req.Priority, SkipMixedChannelCheck: true,
			})
			if err != nil {
				return 0, err
			}
			return account.ID, nil
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"account_id": id}, nil
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var saved struct {
		AccountID int64 `json:"account_id"`
	}
	raw, _ := json.Marshal(result.Data)
	if json.Unmarshal(raw, &saved) != nil || saved.AccountID < 1 {
		response.ErrorFrom(c, service.ErrIdempotencyStoreUnavail)
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), saved.AccountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !strings.EqualFold(account.GetCredential("email"), strings.TrimSpace(req.Email)) || account.Extra["auth_provider"] != "siwc" {
		response.ErrorFrom(c, infraerrors.Conflict("SIWC_IMPORTED_ACCOUNT_CHANGED", "imported account identity changed"))
		return
	}
	expires := account.GetCredentialAsTime("expires_at")
	if expires == nil {
		response.ErrorFrom(c, infraerrors.Conflict("SIWC_TOKEN_EXPIRY_MISSING", "token expiry missing"))
		return
	}
	if result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, map[string]any{
		"account": map[string]any{"id": account.ID, "name": account.Name, "extra": account.Extra},
		"tokens": map[string]any{
			"access_token": account.GetCredential("access_token"), "refresh_token": account.GetCredential("refresh_token"), "id_token": account.GetCredential("id_token"),
			"client_id": account.GetCredential("client_id"), "auth_mode": "siwc", "email": account.GetCredential("email"), "subject": account.GetCredential("subject"),
			"ext_agent_host_id": account.GetCredential("ext_agent_host_id"), "expires_at": expires.Unix(),
			"granted_scopes": account.Credentials["granted_scopes"], "plan_use_enabled": account.Credentials["plan_use_enabled"],
		},
	})
}
