package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountRequestRPMHTTPRetriesAndFailover(t *testing.T) {
	counts := make(map[int64]int)
	tracker := &accountRequestRPMTracker{record: func(id int64) { counts[id]++ }}
	for _, id := range []int64{1, 1, 1, 2, 2, 1} {
		tracker.track(1, id)
	}
	require.Equal(t, map[int64]int{1: 1, 2: 1}, counts)
	otherRequest := &accountRequestRPMTracker{record: func(id int64) { counts[id]++ }}
	otherRequest.track(1, 1)
	require.Equal(t, 2, counts[1])
}

func TestAccountRequestRPMWSTurnsReconnectAndFailover(t *testing.T) {
	counts := make(map[int64]int)
	tracker := &accountRequestRPMTracker{record: func(id int64) { counts[id]++ }}
	first := tracker.beginWSAttempt(1)
	first(1)
	first(2)
	first(2) // Internal reconnect during turn 2.
	require.Equal(t, 2, counts[1])
	retry := tracker.beginWSAttempt(1)
	retry(1) // The relay restarts numbering while retrying logical turn 2.
	require.Equal(t, 2, counts[1])
	failover := tracker.beginWSAttempt(2)
	failover(1)
	require.Equal(t, map[int64]int{1: 2, 2: 1}, counts)
	back := tracker.beginWSAttempt(1)
	back(1)
	require.Equal(t, 2, counts[1], "A -> B -> A in one logical turn still counts once per account")
	back(2) // Logical turn 3 on the recovered connection.
	back(3)
	first(2) // Late callbacks from an older attempt are ignored.
	require.Equal(t, map[int64]int{1: 4, 2: 1}, counts)
	require.Len(t, tracker.accounts, 1, "only the current logical turn is retained")
}

func TestAccountRequestRPMConcurrentRetryCallbacks(t *testing.T) {
	count := 0
	tracker := &accountRequestRPMTracker{record: func(int64) { count++ }}
	started := tracker.beginWSAttempt(1)
	var wg sync.WaitGroup
	for range 1000 {
		wg.Add(1)
		go func() { defer wg.Done(); started(1) }()
	}
	wg.Wait()
	require.Equal(t, 1, count)
}

func TestAccountRequestRPMWSCountsBeforeCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			received, finish := make(chan struct{}, 2), make(chan struct{}, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				for turn := 1; turn <= 2; {
					_, payload, err := conn.Read(ctx)
					if err != nil {
						return
					}
					if gjson.GetBytes(payload, "type").String() != "response.create" {
						continue
					}
					received <- struct{}{}
					select {
					case <-finish:
					case <-ctx.Done():
						return
					}
					event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_rpm_%d","model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`, turn)
					if err := conn.Write(ctx, coderws.MessageText, []byte(event)); err != nil {
						return
					}
					turn++
				}
				<-ctx.Done()
			}))
			t.Cleanup(upstream.Close)
			harness := newOpenAIWSPassthroughHandlerHarnessMode(t, upstream.URL, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			for turn := 1; turn <= 2; turn++ {
				payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi","stream":true}`)
				require.NoError(t, harness.clientConn.Write(ctx, coderws.MessageText, payload))
				select {
				case <-received:
				case <-ctx.Done():
					t.Fatal("upstream did not receive the request")
				}
				select {
				case accountID := <-harness.requestStarts:
					require.EqualValues(t, 9951, accountID)
				default:
					t.Fatal("request must count while the stream is still in progress")
				}
				require.Empty(t, harness.requestStarts, "multiple callbacks must not double count")
				finish <- struct{}{}
				for {
					_, event, err := harness.clientConn.Read(ctx)
					require.NoError(t, err)
					if gjson.GetBytes(event, "type").String() == "response.completed" {
						break
					}
				}
			}
			require.NoError(t, harness.clientConn.CloseNow())
		})
	}
}
