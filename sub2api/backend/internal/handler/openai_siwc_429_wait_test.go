package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/require"
)

func siwcTestAccount() *service.Account {
	return &service.Account{
		ID:          7,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"auth_mode": service.OpenAIAuthModeSIWC},
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestOpenAISIWC429Wait_OnlyArmsOnSIWC429(t *testing.T) {
	rateLimited := &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}
	codex := &service.Account{ID: 8, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}

	var w openAISIWC429Wait
	w.observe(codex, rateLimited)
	_, ok := w.wait(canceledContext())
	require.False(t, ok, "non-SIWC 429 must keep failing fast")

	w.observe(siwcTestAccount(), &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway})
	_, ok = w.wait(canceledContext())
	require.False(t, ok, "SIWC non-429 failures must not wait")

	w.observe(siwcTestAccount(), rateLimited)
	w.observe(siwcTestAccount(), &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway})
	_, ok = w.wait(canceledContext())
	require.False(t, ok, "only the latest failover decides")
}

func TestOpenAISIWC429Wait_HonorsRetryAfterAndRearms(t *testing.T) {
	var w openAISIWC429Wait
	w.observe(siwcTestAccount(), &service.UpstreamFailoverError{
		StatusCode:      http.StatusTooManyRequests,
		ResponseHeaders: http.Header{"Retry-After": []string{"3"}},
	})

	delay, ok := w.wait(canceledContext())
	require.True(t, ok)
	require.Equal(t, 3*time.Second, delay)

	_, ok = w.wait(canceledContext())
	require.False(t, ok, "another pass needs a fresh SIWC 429")
}

func TestOpenAISIWC429Wait_SaturatedOnlyAfterFirstWait(t *testing.T) {
	rateLimited := &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}
	var w openAISIWC429Wait

	w.observe(siwcTestAccount(), rateLimited)
	require.False(t, w.saturated(), "the first pass must still try other accounts")

	_, ok := w.wait(canceledContext())
	require.True(t, ok)
	w.observe(siwcTestAccount(), rateLimited)
	require.True(t, w.saturated(), "after a full pass, a further 429 waits instead of walking all accounts")

	w.observe(siwcTestAccount(), &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway})
	require.False(t, w.saturated())
}

func TestOpenAISIWC429Wait_StopsAfterBudget(t *testing.T) {
	w := openAISIWC429Wait{deadline: time.Now().Add(-time.Second)}
	w.observe(siwcTestAccount(), &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests})

	_, ok := w.wait(canceledContext())
	require.False(t, ok)
}
