package handler

import (
	"sync"

	"github.com/gin-gonic/gin"
)

const accountRequestRPMContextKey = "account_request_rpm_tracker"

// One tracker belongs to one HTTP request or client WS session. Only the current
// WS turn is retained, keeping long-lived connections bounded in memory.
type accountRequestRPMTracker struct {
	mu       sync.Mutex
	turn     int
	accounts []int64
	record   func(int64)
}

func (t *accountRequestRPMTracker) track(turn int, accountID int64) {
	if t == nil || turn <= 0 || accountID <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if turn < t.turn {
		return
	}
	if turn > t.turn {
		t.turn = turn
		t.accounts = t.accounts[:0]
	}
	for _, seen := range t.accounts {
		if seen == accountID {
			return
		}
	}
	t.accounts = append(t.accounts, accountID)
	t.record(accountID)
}

// The relay numbers turns from 1 again on reconnect/failover. Anchor each attempt
// to the current logical turn so retrying a later turn does not count it twice.
func (t *accountRequestRPMTracker) beginWSAttempt(accountID int64) func(int) {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	base := max(1, t.turn)
	t.mu.Unlock()
	started := func(turn int) {
		if turn > 0 {
			t.track(base+turn-1, accountID)
		}
	}
	started(1)
	return started
}

func (h *ConcurrencyHelper) accountRequestRPMTracker(c *gin.Context) *accountRequestRPMTracker {
	if h == nil || h.concurrencyService == nil || c == nil {
		return nil
	}
	if value, ok := c.Get(accountRequestRPMContextKey); ok {
		tracker, _ := value.(*accountRequestRPMTracker)
		return tracker
	}
	tracker := &accountRequestRPMTracker{record: h.concurrencyService.RecordAccountRequest}
	c.Set(accountRequestRPMContextKey, tracker)
	return tracker
}

func (h *ConcurrencyHelper) TrackAccountRequest(c *gin.Context, accountID int64) {
	h.accountRequestRPMTracker(c).track(1, accountID)
}
