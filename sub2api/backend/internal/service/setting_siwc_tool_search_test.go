package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

type toolSearchFailingRepo struct {
	*panelRateLimitSettingRepo
	fail bool
}

func (r *toolSearchFailingRepo) Set(ctx context.Context, key, value string) error {
	if r.fail {
		return errors.New("write unavailable")
	}
	return r.panelRateLimitSettingRepo.Set(ctx, key, value)
}

func TestSIWCToolSearchSettings(t *testing.T) {
	ctx := context.Background()
	r := &toolSearchFailingRepo{panelRateLimitSettingRepo: &panelRateLimitSettingRepo{}}
	s := newPanelRateLimitTestService(r)
	enabled, err := s.GetSIWCToolSearchCompatibility(ctx)
	require.NoError(t, err)
	require.False(t, enabled)
	require.NoError(t, s.SetSIWCToolSearchCompatibility(ctx, true))
	for i := 0; i < 100; i++ {
		enabled, err = s.GetSIWCToolSearchCompatibility(ctx)
		require.NoError(t, err)
		require.True(t, enabled)
	}
	require.Equal(t, 1, r.getValueCalls, "cached requests must not read DB")
	enabled, err = newPanelRateLimitTestService(r).GetSIWCToolSearchCompatibility(ctx)
	require.NoError(t, err)
	require.True(t, enabled, "persist across restart")
	r.fail = true
	require.Error(t, s.SetSIWCToolSearchCompatibility(ctx, false))
	enabled, err = s.GetSIWCToolSearchCompatibility(ctx)
	require.NoError(t, err)
	require.True(t, enabled, "failed save must not alter cache")
	r.getValueErr = errors.New("read unavailable")
	s.siwcToolSearchCache.Store(&cachedSIWCToolSearch{enabled: true, expires: time.Now().Add(-time.Second)})
	enabled, err = s.GetSIWCToolSearchCompatibility(ctx)
	require.Error(t, err)
	require.True(t, enabled, "retain last good setting on transient DB failure")
	r.fail = false
	require.NoError(t, s.SetSIWCToolSearchCompatibility(ctx, false))
	enabled, err = s.GetSIWCToolSearchCompatibility(ctx)
	require.NoError(t, err)
	require.False(t, enabled)
}

func TestSIWCToolSearchConcurrentReadSave(t *testing.T) {
	s := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = s.SetSIWCToolSearchCompatibility(context.Background(), i%4 == 0)
			} else {
				_, _ = s.GetSIWCToolSearchCompatibility(context.Background())
			}
		}(i)
	}
	wg.Wait()
	require.NoError(t, s.SetSIWCToolSearchCompatibility(context.Background(), true))
	got, err := s.GetSIWCToolSearchCompatibility(context.Background())
	require.NoError(t, err)
	require.True(t, got)
	persisted, err := newPanelRateLimitTestService(s.settingRepo).GetSIWCToolSearchCompatibility(context.Background())
	require.NoError(t, err)
	require.Equal(t, got, persisted)
}
