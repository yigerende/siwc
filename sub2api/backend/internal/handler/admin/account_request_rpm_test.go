package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRequestRPMListCache struct {
	service.ConcurrencyCache
	counts map[int64]int
	reads  [][]int64
	fail   bool
}

func (*accountRequestRPMListCache) GetAccountConcurrencyBatch(context.Context, []int64) (map[int64]int, error) {
	return map[int64]int{}, nil
}

func (*accountRequestRPMListCache) WriteAccountRequestRPM(context.Context, string, int64, []service.AccountRequestRPMSnapshot) error {
	return nil
}

func (c *accountRequestRPMListCache) GetAccountRequestRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	c.reads = append(c.reads, append([]int64(nil), ids...))
	if c.fail {
		return nil, errors.New("cache unavailable")
	}
	return c.counts, nil
}

func TestAccountListRequestRPMBatchAndETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, suffix := range []string{"", "?lite=1"} {
		t.Run(suffix, func(t *testing.T) {
			adminSvc := newStubAdminService()
			adminSvc.accounts = []service.Account{{ID: 1, Platform: service.PlatformOpenAI}, {ID: 2, Platform: service.PlatformGemini}}
			cache := &accountRequestRPMListCache{counts: map[int64]int{1: 0, 2: 3}}
			h := &AccountHandler{adminService: adminSvc, concurrencyService: service.NewConcurrencyService(cache)}
			router := gin.New()
			router.GET("/accounts", h.List)
			get := func(etag string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "/accounts"+suffix, nil)
				req.Header.Set("If-None-Match", etag)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				return rec
			}
			decode := func(rec *httptest.ResponseRecorder) []map[string]any {
				var payload struct {
					Data struct {
						Items []map[string]any `json:"items"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
				return payload.Data.Items
			}
			first := get("")
			require.Equal(t, http.StatusOK, first.Code)
			require.Equal(t, [][]int64{{1, 2}}, cache.reads)
			require.Equal(t, float64(0), decode(first)[0]["request_rpm"])
			require.Equal(t, float64(3), decode(first)[1]["request_rpm"])
			require.Equal(t, http.StatusNotModified, get(first.Header().Get("ETag")).Code)
			cache.counts[1] = 42
			changed := get(first.Header().Get("ETag"))
			require.Equal(t, http.StatusOK, changed.Code)
			require.Equal(t, float64(42), decode(changed)[0]["request_rpm"])
			cache.fail = true
			unavailable := get("")
			require.Equal(t, http.StatusOK, unavailable.Code)
			item := decode(unavailable)[0]
			require.Contains(t, item, "request_rpm")
			require.Nil(t, item["request_rpm"])
		})
	}
}
