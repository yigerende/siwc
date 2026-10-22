package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAISIWC429WaitBudget bounds how long one request may wait for SIWC
// per-account rate limits to reset after every candidate account returned 429.
const openAISIWC429WaitBudget = 2 * time.Minute

// openAISIWC429Wait turns "every account is rate limited" into a short pause
// followed by another pass over all accounts instead of failing the request.
type openAISIWC429Wait struct {
	last     *service.UpstreamFailoverError
	deadline time.Time
	waited   bool
}

// observe records the latest failover; only a SIWC 429 arms the wait.
func (w *openAISIWC429Wait) observe(account *service.Account, failoverErr *service.UpstreamFailoverError) {
	w.last = nil
	if account.IsOpenAISIWC() && failoverErr != nil && failoverErr.StatusCode == http.StatusTooManyRequests {
		w.last = failoverErr
	}
}

// saturated reports a SIWC 429 after every account has already been tried and
// waited on once; walking all accounts again would only multiply upstream 429s.
func (w *openAISIWC429Wait) saturated() bool {
	return w.waited && w.last != nil
}

// wait pauses before the caller retries all accounts. It reports false when the
// latest failover was not a SIWC 429 or the request's wait budget is spent.
func (w *openAISIWC429Wait) wait(ctx context.Context) (time.Duration, bool) {
	if w.last == nil {
		return 0, false
	}
	if w.deadline.IsZero() {
		w.deadline = time.Now().Add(openAISIWC429WaitBudget)
	}
	if !time.Now().Before(w.deadline) {
		return 0, false
	}
	delay := service.OpenAIOAuth429RetryDelay(w.last.ResponseHeaders, w.deadline)
	w.last = nil
	w.waited = true
	// A canceled context ends the sleep early; the caller's loop-top client
	// check then stops the request.
	_ = sleepWithContext(ctx, delay)
	return delay, true
}
