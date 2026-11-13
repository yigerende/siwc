package service

import (
	"context"
	"errors"
	"strconv"
	"time"
)

const SettingKeySIWCToolSearch = "siwc_tool_search_compat_enabled"

type cachedSIWCToolSearch struct {
	enabled bool
	err     error
	expires time.Time
}

// GetSIWCToolSearchCompatibility defaults to off. Reads and writes share
// a lock so an older DB read cannot overwrite a freshly saved toggle. Hot reads
// are lock-free; DB errors retain the last known value and retry after five seconds.
func (s *SettingService) GetSIWCToolSearchCompatibility(ctx context.Context) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, nil
	}
	if c := s.siwcToolSearchCache.Load(); c != nil && time.Now().Before(c.expires) {
		return c.enabled, c.err
	}
	s.siwcToolSearchMu.Lock()
	defer s.siwcToolSearchMu.Unlock()
	previous := s.siwcToolSearchCache.Load()
	if previous != nil && time.Now().Before(previous.expires) {
		return previous.enabled, previous.err
	}
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeySIWCToolSearch)
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
	s.siwcToolSearchCache.Store(&cachedSIWCToolSearch{enabled: enabled, err: err, expires: time.Now().Add(ttl)})
	return enabled, err
}

func (s *SettingService) SetSIWCToolSearchCompatibility(ctx context.Context, enabled bool) error {
	s.siwcToolSearchMu.Lock()
	defer s.siwcToolSearchMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeySIWCToolSearch, strconv.FormatBool(enabled)); err != nil {
		return err
	}
	s.siwcToolSearchCache.Store(&cachedSIWCToolSearch{enabled: enabled, expires: time.Now().Add(time.Minute)})
	return nil
}
