package service

import (
	"context"
	"errors"
	"strconv"
	"time"
)

const SettingKeySIWCAlphaSearch = "siwc_alpha_search_compat_enabled"

type cachedSIWCAlphaSearch struct {
	enabled bool
	err     error
	expires time.Time
}

// GetSIWCAlphaSearchCompatibility defaults to off. Reads and writes share
// a lock so an older DB read cannot overwrite a freshly saved toggle. Hot reads
// are lock-free; DB errors retain the last known value and retry after five seconds.
func (s *SettingService) GetSIWCAlphaSearchCompatibility(ctx context.Context) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, nil
	}
	if c := s.siwcAlphaSearchCache.Load(); c != nil && time.Now().Before(c.expires) {
		return c.enabled, c.err
	}
	s.siwcAlphaSearchMu.Lock()
	defer s.siwcAlphaSearchMu.Unlock()
	previous := s.siwcAlphaSearchCache.Load()
	if previous != nil && time.Now().Before(previous.expires) {
		return previous.enabled, previous.err
	}
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeySIWCAlphaSearch)
	if errors.Is(err, ErrSettingNotFound) {
		err = nil
	}
	enabled := false
	if err == nil && raw != "" {
		enabled, err = strconv.ParseBool(raw)
	}
	ttl := time.Minute
	if err != nil {
		ttl = 5 * time.Second
		if previous != nil {
			enabled = previous.enabled
		}
	}
	s.siwcAlphaSearchCache.Store(&cachedSIWCAlphaSearch{enabled: enabled, err: err, expires: time.Now().Add(ttl)})
	return enabled, err
}

func (s *SettingService) SetSIWCAlphaSearchCompatibility(ctx context.Context, enabled bool) error {
	s.siwcAlphaSearchMu.Lock()
	defer s.siwcAlphaSearchMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeySIWCAlphaSearch, strconv.FormatBool(enabled)); err != nil {
		return err
	}
	s.siwcAlphaSearchCache.Store(&cachedSIWCAlphaSearch{enabled: enabled, expires: time.Now().Add(time.Minute)})
	return nil
}
