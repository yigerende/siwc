package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type spaceWorkflowAdminStub struct {
	service.AdminService
	calls int
}

func (s *spaceWorkflowAdminStub) SaveSpaceWorkflow(_ context.Context, _ int64, v service.SpaceWorkflow) (*service.SpaceWorkflowReceipt, error) {
	s.calls++
	return &service.SpaceWorkflowReceipt{Revision: v.Revision, Applied: true}, nil
}
func handlerSpaceMarker() *service.SpaceWorkflow {
	return &service.SpaceWorkflow{SourceID: "0123456789abcdef0123456789abcdef", Revision: 1, Email: "a@example.com", FlowID: "flow", Stage: "merge_v2", Completed: true, ChangedAt: time.Now().UTC()}
}

func TestSpaceWorkflowHandlerValidatesBeforeWriting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &spaceWorkflowAdminStub{}
	h := &AccountHandler{adminService: svc}
	r := gin.New()
	r.PUT("/accounts/:id/space-workflow", h.SaveSpaceWorkflow)
	raw, _ := json.Marshal(handlerSpaceMarker())
	for _, tc := range []struct {
		id     string
		body   []byte
		status int
	}{{"7", raw, 200}, {"0", raw, 400}, {"7", []byte(`{"stage":"quota"}`), 400}, {"7", bytes.Repeat([]byte("x"), 5000), 400}} {
		req := httptest.NewRequest("PUT", "/accounts/"+tc.id+"/space-workflow", bytes.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		require.Equal(t, tc.status, res.Code, res.Body.String())
	}
	require.Equal(t, 1, svc.calls)
}

func TestSpaceWorkflowVisibleInFullLiteListsAndETag(t *testing.T) {
	for _, suffix := range []string{"", "&lite=1"} {
		t.Run(suffix, func(t *testing.T) {
			r, svc := setupAccountListRouter()
			m := handlerSpaceMarker()
			svc.accounts = []service.Account{{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, SpaceWorkflow: m}}
			get := func(etag string) *httptest.ResponseRecorder {
				req := httptest.NewRequest("GET", "/api/v1/admin/accounts?page=1&page_size=20"+suffix, nil)
				req.Header.Set("If-None-Match", etag)
				res := httptest.NewRecorder()
				r.ServeHTTP(res, req)
				return res
			}
			first := get("")
			require.Equal(t, http.StatusOK, first.Code)
			require.Contains(t, first.Body.String(), `"space_workflow"`)
			require.Contains(t, first.Body.String(), `"completed":true`)
			require.Equal(t, http.StatusNotModified, get(first.Header().Get("ETag")).Code)
			m.Completed = false
			m.Revision++
			changed := get(first.Header().Get("ETag"))
			require.Equal(t, http.StatusOK, changed.Code)
			require.Contains(t, changed.Body.String(), `"completed":false`)
		})
	}
}
