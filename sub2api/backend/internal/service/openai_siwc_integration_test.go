package service

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestSIWCIntegrationExchangeFailureAllowsOnlyNewAuthorization(t *testing.T) {
	svc, client, id, callback := integrationFixture(t)
	client.exchangeErr = context.DeadlineExceeded
	for range 2 {
		_, err := svc.ImportSIWCIntegration(context.Background(), "admin:1", id, callback, "same", func(*OpenAITokenInfo) (int64, error) { t.Fatal("create called after failed exchange"); return 0, nil })
		app := infraerrors.FromError(err)
		require.Equal(t, "SIWC_CALLBACK_UNCONFIRMED", app.Reason)
		require.Equal(t, "true", app.Metadata["reauthorize_allowed"])
		require.Equal(t, "exchange", app.Metadata["failure_stage"])
		require.Equal(t, "timeout", app.Metadata["failure_category"])
	}
	require.Equal(t, 1, client.exchangeCalls)
}

func TestSIWCIntegrationFailureMetadataDoesNotLeakSecrets(t *testing.T) {
	err := siwcIntegrationFailure("exchange", errors.New("code=private-code access_token=private-token proxy=http://user:password@host"), true)
	require.NotContains(t, err.Error(), "private")
	require.False(t, strings.Contains(err.Error(), "password"))
}

func integrationFixture(t *testing.T) (*OpenAIOAuthService, *siwcFlowTestClient, string, string) {
	t.Helper()
	client := &siwcFlowTestClient{tokenResp: &openai.TokenResponse{AccessToken: "at", RefreshToken: "rt", IDToken: "valid-id-token", ExpiresIn: 3600, Scope: openai.SIWCPlanUseScope}}
	svc := newSIWCFlowService(client)
	t.Cleanup(svc.Stop)
	result, err := svc.GenerateSIWCIntegration(context.Background(), "admin:1", "user@example.com", &SIWCAuthURLInput{RedirectURI: "http://127.0.0.1:1456/auth/callback", ProxyURL: "http://user:private@proxy.example:1234"})
	require.NoError(t, err)
	u, _ := url.Parse(result.AuthURL)
	callback := "http://127.0.0.1:1456/auth/callback?" + url.Values{"state": {u.Query().Get("state")}, "code": {"private-code"}, "client_id": {"oaiapp_issued"}}.Encode()
	return svc, client, result.SessionID, callback
}
func TestSIWCIntegrationConcurrentReplayAndPinnedProxy(t *testing.T) {
	svc, client, id, callback := integrationFixture(t)
	var creates atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := svc.ImportSIWCIntegration(context.Background(), "admin:1", id, callback, "same", func(*OpenAITokenInfo) (int64, error) { creates.Add(1); return 42, nil })
			if err == nil && result != 42 {
				t.Error("wrong account")
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), creates.Load())
	require.Equal(t, 1, client.exchangeCalls)
	require.Equal(t, "http://user:private@proxy.example:1234", client.lastProxyURL)
	result, err := svc.ImportSIWCIntegration(context.Background(), "admin:1", id, callback, "same", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), result)
	_, err = svc.ImportSIWCIntegration(context.Background(), "admin:1", id, callback, "changed-groups", nil)
	require.Error(t, err)
}
func TestSIWCIntegrationRejectsInvalidCallbackBeforeExchange(t *testing.T) {
	for _, change := range []func(string) string{
		func(s string) string { return s + "&state=duplicate" },
		func(s string) string { return s + "#fragment" },
		func(s string) string { return "https://evil.example/?" + s },
		func(s string) string { return s + "&error=access_denied" },
	} {
		svc, client, id, callback := integrationFixture(t)
		_, err := svc.ImportSIWCIntegration(context.Background(), "admin:1", id, change(callback), "same", nil)
		require.Error(t, err)
		require.Zero(t, client.exchangeCalls)
	}
	svc, client, id, callback := integrationFixture(t)
	_, err := svc.ImportSIWCIntegration(context.Background(), "admin:2", id, callback, "same", nil)
	require.Error(t, err)
	require.Zero(t, client.exchangeCalls)
}
func TestSIWCIntegrationFailedImportDoesNotExchangeOrCreateAgain(t *testing.T) {
	svc, client, id, callback := integrationFixture(t)
	creates := 0
	create := func(*OpenAITokenInfo) (int64, error) { creates++; return 0, errors.New("database response lost") }
	for i := 0; i < 2; i++ {
		_, err := svc.ImportSIWCIntegration(context.Background(), "admin:1", id, callback, "same", create)
		require.Error(t, err)
		require.Equal(t, "false", infraerrors.FromError(err).Metadata["reauthorize_allowed"])
	}
	require.Equal(t, 1, creates)
	require.Equal(t, 1, client.exchangeCalls)
}
