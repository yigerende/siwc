package service

import (
	"context"
	"crypto/subtle"
	"net/url"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Per-session serialization prevents the one-use code or a partial create from
// being submitted twice. The persistent idempotency result contains only an ID.
type siwcIntegrationSession struct {
	mu                        sync.Mutex
	owner, email, fingerprint string
	created                   time.Time
	attempted                 bool
	accountID                 int64
	outcome                   error
}

func (s *OpenAIOAuthService) GenerateSIWCIntegration(ctx context.Context, owner, email string, input *SIWCAuthURLInput) (*SIWCAuthURLResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, infraerrors.BadRequest("SIWC_EMAIL_REQUIRED", "expected email is required")
	}
	result, err := s.GenerateSIWCAuthURL(ctx, input)
	if err != nil {
		return nil, err
	}
	s.siwcIntegrations.Range(func(key, value any) bool {
		if time.Since(value.(*siwcIntegrationSession).created) > openai.SessionTTL {
			s.siwcIntegrations.Delete(key)
		}
		return true
	})
	s.siwcIntegrations.Store(result.SessionID, &siwcIntegrationSession{owner: owner, email: email, created: time.Now()})
	return result, nil
}

// The callback is data only: never request its loopback URL. A failed exchange
// may already have consumed the code, so retrying it must not resend the POST.
func (s *OpenAIOAuthService) ImportSIWCIntegration(ctx context.Context, owner, sessionID, callback, fingerprint string, create func(*OpenAITokenInfo) (int64, error)) (int64, error) {
	value, ok := s.siwcIntegrations.Load(sessionID)
	if !ok {
		return 0, infraerrors.BadRequest("SIWC_SESSION_EXPIRED", "integration session expired; start a new authorization")
	}
	entry := value.(*siwcIntegrationSession)
	if !entry.mu.TryLock() {
		return 0, infraerrors.Conflict("SIWC_CALLBACK_IN_PROGRESS", "callback is still processing")
	}
	defer entry.mu.Unlock()
	if entry.owner != owner || time.Since(entry.created) > openai.SessionTTL {
		return 0, infraerrors.BadRequest("SIWC_SESSION_EXPIRED", "integration session expired")
	}
	if entry.fingerprint != "" && entry.fingerprint != fingerprint {
		return 0, ErrIdempotencyKeyConflict
	}
	if entry.attempted {
		return entry.accountID, entry.outcome
	}
	session, ok := s.sessionStore.Get(sessionID)
	if !ok {
		return 0, infraerrors.BadRequest("SIWC_SESSION_EXPIRED", "integration session expired")
	}
	parsed, err := url.Parse(callback)
	redirect, _ := url.Parse(session.RedirectURI)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Scheme != redirect.Scheme || parsed.Host != redirect.Host || parsed.Path != redirect.Path || parsed.RawPath != redirect.RawPath {
		return 0, infraerrors.BadRequest("SIWC_INVALID_CALLBACK", "callback does not match the authorization redirect")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || query.Has("error") {
		return 0, infraerrors.BadRequest("SIWC_INVALID_CALLBACK", "authorization did not complete")
	}
	for _, key := range []string{"code", "state", "client_id"} {
		if len(query[key]) != 1 || strings.TrimSpace(query.Get(key)) == "" {
			return 0, infraerrors.BadRequest("SIWC_INVALID_CALLBACK", "callback fields missing or duplicated")
		}
	}
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(session.State)) != 1 {
		return 0, infraerrors.BadRequest("SIWC_INVALID_STATE", "callback state mismatch")
	}
	if query.Get("client_id") == openai.SIWCRegistrationClientID {
		return 0, infraerrors.BadRequest("SIWC_INVALID_CLIENT", "callback has no issued client ID")
	}
	entry.fingerprint, entry.attempted = fingerprint, true
	// Keep a deterministic safe failure even if a panic interrupts downstream code.
	entry.outcome = infraerrors.Conflict("SIWC_CALLBACK_UNCONFIRMED", "callback result unconfirmed; do not submit another import")
	tokens, err := s.ExchangeSIWCCode(ctx, &SIWCExchangeInput{SessionID: sessionID, Code: query.Get("code"), State: query.Get("state"), IssuedClientID: query.Get("client_id")})
	if err != nil {
		return 0, entry.outcome
	}
	if !strings.EqualFold(strings.TrimSpace(tokens.Email), entry.email) || tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.IDToken == "" || tokens.ExpiresAt <= time.Now().Unix() || !tokens.PlanUseEnabled {
		entry.outcome = infraerrors.BadRequest("SIWC_TOKEN_NOT_USABLE", "SIWC token identity, completeness or subscription scope validation failed")
		return 0, entry.outcome
	}
	id, err := create(tokens)
	if err != nil {
		return 0, entry.outcome
	}
	entry.accountID, entry.outcome = id, nil
	return id, nil
}
