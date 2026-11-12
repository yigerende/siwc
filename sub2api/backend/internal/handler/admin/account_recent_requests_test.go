//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRecentRepoStub struct {
	service.UsageLogRepository
	calls int
}

func (r *accountRecentRepoStub) LatestAccountRequests(ctx context.Context, input service.AccountRecentRequestsInput) ([]service.AccountRecentRequests, error) {
	r.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("missing query deadline")
	}
	return []service.AccountRecentRequests{{AccountID: input.AccountIDs[0], Requests: []service.AccountRecentRequest{}}}, nil
}

func TestAccountRecentRequestsHandlerValidationAndLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &accountRecentRepoStub{}
	h := &UsageHandler{usageService: service.NewUsageService(repo, nil, nil, nil)}
	router := gin.New()
	router.POST("/recent", h.AccountRecentRequests)
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/recent", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	body := `{"account_ids":[1,2]}`
	require.Equal(t, 200, call(body).Code)
	require.Equal(t, 1, repo.calls)
	for _, invalid := range []string{`{}`, `{"account_ids":[1,1]}`, `{"account_ids":[0]}`, strings.Repeat("x", 17000)} {
		require.Equal(t, 400, call(invalid).Code)
	}
	accountRecentRequestSlots <- struct{}{}
	accountRecentRequestSlots <- struct{}{}
	defer func() { <-accountRecentRequestSlots; <-accountRecentRequestSlots }()
	w := call(body)
	require.Equal(t, 429, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Equal(t, 1, repo.calls)
}
