package admin

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSIWCRemoteURLAndDestinationValidation(t *testing.T) {
	for _, raw := range []string{"https://example.com/", "https://example.com/api/v1/", "http://127.0.0.1:10099"} {
		value, err := normalizeSIWCRemoteURL(raw)
		require.NoError(t, err)
		require.NotContains(t, value, "/api/v1")
	}
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com", "https://user:password@example.com", "https://example.com/admin", "https://example.com?next=x", "https://example.com#x", "https://example.com/%2e%2e"} {
		_, err := normalizeSIWCRemoteURL(raw)
		require.Error(t, err, raw)
	}
	for _, raw := range []string{"169.254.169.254", "100.100.100.200", "fe80::1", "0.0.0.0", "224.0.0.1", "::ffff:169.254.169.254"} {
		require.False(t, siwcRemoteIPAllowed(net.ParseIP(raw)), raw)
	}
	for _, raw := range []string{"127.0.0.1", "::1", "192.168.1.2", "8.8.8.8"} {
		require.True(t, siwcRemoteIPAllowed(net.ParseIP(raw)), raw)
	}
}

func siwcRemoteTestRouter(h *SIWCRemoteHandler) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		id := int64(1)
		if c.GetHeader("X-Test-Owner") == "other" {
			id = 2
		}
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: id})
	})
	r.POST("/connect", h.Connect)
	r.POST("/:target/generate", h.Generate)
	r.POST("/:target/import", h.Import)
	r.DELETE("/:target", h.Disconnect)
	return r
}

func siwcRemoteTestRequest(r *gin.Engine, method, path, body, owner string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Owner", owner)
	r.ServeHTTP(w, req)
	return w
}

func TestSIWCRemoteFlowBindsOwnerAndGroupsAndDoesNotExposeTokens(t *testing.T) {
	imports := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var data any
		switch r.URL.Path {
		case "/api/v1/auth/login":
			require.Empty(t, r.Header.Get("Authorization"))
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "test-password", body["password"])
			data = gin.H{"access_token": "secret-admin-token", "refresh_token": "secret-refresh", "expires_in": 3600}
		case "/api/v1/admin/groups/all":
			require.Equal(t, "Bearer secret-admin-token", r.Header.Get("Authorization"))
			require.Equal(t, "openai", r.URL.Query().Get("platform"))
			data = []siwcRemoteGroup{{ID: 2, Name: "A", Platform: "openai", Status: "active"}, {ID: 3, Name: "B", Platform: "openai", Status: "active"}, {ID: 4, Platform: "anthropic", Status: "active"}}
		case "/api/v1/admin/openai/siwc/generate-auth-url":
			var body OpenAISIWCGenerateAuthURLRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, siwcManualRedirect, body.RedirectURI)
			data = gin.H{"auth_url": "https://auth.openai.com/api/accounts/authorize?state=expected&redirect_uri=" + url.QueryEscape(siwcManualRedirect), "session_id": "remote-oauth-session", "registration": true}
		case "/api/v1/admin/openai/siwc/create-from-oauth":
			imports++
			var body OpenAISIWCCreateAccountRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, []int64{2, 3}, body.GroupIDs)
			require.Equal(t, siwcManualRedirect, body.RedirectURI)
			require.Nil(t, body.ProxyID)
			data = gin.H{"id": 45, "name": "imported", "credentials": gin.H{"access_token": "secret-account-token"}}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(gin.H{"code": 0, "data": data})
	}))
	defer upstream.Close()
	h := NewSIWCRemoteHandler()
	r := siwcRemoteTestRouter(h)
	w := siwcRemoteTestRequest(r, "POST", "/connect", `{"url":"`+upstream.URL+`/api/v1/","email":"admin@example.com","password":"test-password"}`, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "secret-")
	require.NotContains(t, w.Body.String(), "test-password")
	var connected struct {
		Data struct {
			ID     string            `json:"target_id"`
			Groups []siwcRemoteGroup `json:"groups"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &connected))
	id := connected.Data.ID
	require.Len(t, connected.Data.Groups, 2)
	w = siwcRemoteTestRequest(r, "POST", "/"+id+"/generate", `{"group_ids":[2,3]}`, "other")
	require.Equal(t, 400, w.Code)
	w = siwcRemoteTestRequest(r, "POST", "/"+id+"/generate", `{"group_ids":[4]}`, "")
	require.Equal(t, 400, w.Code)
	w = siwcRemoteTestRequest(r, "POST", "/"+id+"/generate", `{"group_ids":[2,3]}`, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	body := `{"session_id":"remote-oauth-session","code":"fake-code","state":"expected","issued_client_id":"issued","group_ids":[999],"redirect_uri":"http://wrong","proxy_id":99}`
	w = siwcRemoteTestRequest(r, "POST", "/"+id+"/import", strings.Replace(body, "expected", "wrong", 1), "")
	require.Equal(t, 400, w.Code)
	require.Zero(t, imports)
	w = siwcRemoteTestRequest(r, "POST", "/"+id+"/import", body, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "secret-")
	w = siwcRemoteTestRequest(r, "POST", "/"+id+"/import", body, "")
	require.Equal(t, 400, w.Code)
	require.Equal(t, 1, imports)
	h.mu.Lock()
	h.targets[id].Expires = time.Now().Add(-time.Second)
	h.mu.Unlock()
	w = siwcRemoteTestRequest(r, "POST", "/"+id+"/generate", `{"group_ids":[2]}`, "")
	require.Equal(t, 400, w.Code)
}

func TestSIWCRemoteNeverFollowsCredentialRedirects(t *testing.T) {
	delivered := false
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { delivered = true }))
	defer receiver.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	w := siwcRemoteTestRequest(siwcRemoteTestRouter(NewSIWCRemoteHandler()), "POST", "/connect", `{"url":"`+redirector.URL+`","email":"admin@example.com","password":"test-password"}`, "")
	require.Equal(t, 400, w.Code)
	require.False(t, delivered)
	require.NotContains(t, w.Body.String(), "test-password")
}
