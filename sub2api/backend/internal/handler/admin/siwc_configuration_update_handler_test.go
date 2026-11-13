package admin

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type siwcConfigurationUpdateRepo struct {
	service.SettingRepository
	value  string
	fail   bool
	writes int
}

func (r *siwcConfigurationUpdateRepo) GetValue(context.Context, string) (string, error) {
	return r.value, nil
}
func (r *siwcConfigurationUpdateRepo) Set(_ context.Context, _ string, value string) error {
	if r.fail {
		return errors.New("unavailable")
	}
	r.value = value
	r.writes++
	return nil
}

func TestSIWCConfigurationUpdateHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := &siwcConfigurationUpdateRepo{}
	h := NewSIWCConfigurationUpdateHandler(service.NewSettingService(r, &config.Config{}))
	router := gin.New()
	router.GET("/setting", h.Get)
	router.PUT("/setting", h.Save)
	request := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/setting", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}
	w := request("GET", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"enabled":false`)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"true"}`, `{"enabled":1}`, `not json`} {
		require.Equal(t, 400, request("PUT", body).Code)
	}
	require.Zero(t, r.writes)
	require.Equal(t, 200, request("PUT", `{"enabled":true}`).Code)
	require.Equal(t, "true", r.value)
	require.Contains(t, request("GET", "").Body.String(), `"enabled":true`)
	r.fail = true
	require.Equal(t, 500, request("PUT", `{"enabled":false}`).Code)
	require.Contains(t, request("GET", "").Body.String(), `"enabled":true`)
	r.fail = false
	require.Equal(t, 200, request("PUT", `{"enabled":false}`).Code)
	require.Equal(t, "false", r.value)
}
