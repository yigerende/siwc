package service

import (
	"context"
	"errors"
	"strconv"
	"time"
)

const SettingKeySIWCRemoteCompaction = "siwc_remote_compaction_compat_enabled"

type cachedSIWCRemoteCompaction struct {
	enabled bool
	err     error
	expires time.Time
}

// GetSIWCRemoteCompactionCompatibility defaults to off. Reads and writes share
// a lock so an older DB read cannot overwrite a freshly saved toggle. Hot reads
// are lock-free; DB errors retain the last known value and retry after five seconds.
func (s *SettingService) GetSIWCRemoteCompactionCompatibility(ctx context.Context) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, nil
	}
	if c := s.siwcRemoteCompactionCache.Load(); c != nil && time.Now().Before(c.expires) {
		return c.enabled, c.err
	}
	s.siwcRemoteCompactionMu.Lock()
	defer s.siwcRemoteCompactionMu.Unlock()
	previous := s.siwcRemoteCompactionCache.Load()
	if previous != nil && time.Now().Before(previous.expires) {
		return previous.enabled, previous.err
	}
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeySIWCRemoteCompaction)
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
	s.siwcRemoteCompactionCache.Store(&cachedSIWCRemoteCompaction{enabled: enabled, err: err, expires: time.Now().Add(ttl)})
	return enabled, err
}

func (s *SettingService) SetSIWCRemoteCompactionCompatibility(ctx context.Context, enabled bool) error {
	s.siwcRemoteCompactionMu.Lock()
	defer s.siwcRemoteCompactionMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeySIWCRemoteCompaction, strconv.FormatBool(enabled)); err != nil {
		return err
	}
	s.siwcRemoteCompactionCache.Store(&cachedSIWCRemoteCompaction{enabled: enabled, expires: time.Now().Add(time.Minute)})
	return nil
}
