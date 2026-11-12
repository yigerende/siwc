package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

type integrationAdminStub struct {
	service.AdminService
	account *service.Account
}

func (s integrationAdminStub) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	return s.account, nil
}
func TestSIWCIntegrationCallbackReplayReadsStoredAccountTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newMemoryIdempotencyRepoStub()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	coordinator := service.NewIdempotencyCoordinator(repo, cfg)
	service.SetDefaultIdempotencyCoordinator(coordinator)
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })
	path := "/api/v1/admin/openai/siwc/integration/callback"
	req := siwcIntegrationCallbackRequest{SessionID: "expired-session", CallbackURL: "http://127.0.0.1:1456/auth/callback?code=private", Email: "user@example.com", GroupIDs: []int64{7}, Concurrency: 10, Priority: 1, WebAccessToken: "web", WebAccountID: "personal"}
	_, err := coordinator.Execute(context.Background(), service.IdempotencyExecuteOptions{Scope: "admin.siwc.integration.import", ActorScope: "admin:0", Method: "POST", Route: path, IdempotencyKey: "original-key", Payload: req, RequireKey: true}, func(context.Context) (any, error) { return map[string]any{"account_id": 42}, nil })
	require.NoError(t, err)
	account := &service.Account{ID: 42, Name: "fixture", Extra: map[string]any{"auth_provider": "siwc"}, Credentials: map[string]any{
		"email": "user@example.com", "access_token": "original-at", "refresh_token": "original-rt", "id_token": "original-id", "client_id": "app", "subject": "subject", "ext_agent_host_id": "host", "granted_scopes": []string{"chatgpt.tokens.use.direct"}, "plan_use_enabled": true, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
	}}
	handler := &OpenAIOAuthHandler{adminService: integrationAdminStub{account: account}}
	router := gin.New()
	router.POST(path, handler.ImportSIWCIntegration)
	call := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(req)
		r := httptest.NewRequest("POST", path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "original-key")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := call()
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "true", w.Header().Get("X-Idempotency-Replayed"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Contains(t, w.Body.String(), "original-at")
	require.Contains(t, w.Body.String(), "original-rt")
	for _, record := range repo.data {
		require.NotContains(t, *record.ResponseBody, "original-at")
		require.NotContains(t, *record.ResponseBody, "private")
	}
	req.GroupIDs = []int64{8}
	require.Equal(t, 409, call().Code)
}
