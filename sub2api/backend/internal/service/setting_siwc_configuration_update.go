package service

import (
	"context"
	"errors"
	"strconv"
	"time"
)

const SettingKeySIWCConfigurationUpdate = "siwc_configuration_update_compat_enabled"

type cachedSIWCConfigurationUpdate struct {
	enabled bool
	err     error
	expires time.Time
}

// GetSIWCConfigurationUpdateCompatibility defaults to off. Reads and writes share
// a lock so an older DB read cannot overwrite a freshly saved toggle. Hot reads
// are lock-free; DB errors retain the last known value and retry after five seconds.
func (s *SettingService) GetSIWCConfigurationUpdateCompatibility(ctx context.Context) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, nil
	}
	if c := s.siwcConfigurationUpdateCache.Load(); c != nil && time.Now().Before(c.expires) {
		return c.enabled, c.err
	}
	s.siwcConfigurationUpdateMu.Lock()
	defer s.siwcConfigurationUpdateMu.Unlock()
	previous := s.siwcConfigurationUpdateCache.Load()
	if previous != nil && time.Now().Before(previous.expires) {
		return previous.enabled, previous.err
	}
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeySIWCConfigurationUpdate)
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
	s.siwcConfigurationUpdateCache.Store(&cachedSIWCConfigurationUpdate{enabled: enabled, err: err, expires: time.Now().Add(ttl)})
	return enabled, err
}

func (s *SettingService) SetSIWCConfigurationUpdateCompatibility(ctx context.Context, enabled bool) error {
	s.siwcConfigurationUpdateMu.Lock()
	defer s.siwcConfigurationUpdateMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeySIWCConfigurationUpdate, strconv.FormatBool(enabled)); err != nil {
		return err
	}
	s.siwcConfigurationUpdateCache.Store(&cachedSIWCConfigurationUpdate{enabled: enabled, expires: time.Now().Add(time.Minute)})
	return nil
}
