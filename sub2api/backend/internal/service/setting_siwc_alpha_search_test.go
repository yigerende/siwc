package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

type alphaSearchSettingFailingRepo struct {
	*panelRateLimitSettingRepo
	fail bool
}

func (r *alphaSearchSettingFailingRepo) Set(ctx context.Context, key, value string) error {
	if r.fail {
		return errors.New("write unavailable")
	}
	return r.panelRateLimitSettingRepo.Set(ctx, key, value)
}

func TestSIWCAlphaSearchSettings(t *testing.T) {
	ctx := context.Background()
	r := &alphaSearchSettingFailingRepo{panelRateLimitSettingRepo: &panelRateLimitSettingRepo{}}
	s := newPanelRateLimitTestService(r)
	enabled, err := s.GetSIWCAlphaSearchCompatibility(ctx)
	require.NoError(t, err)
	require.False(t, enabled)
	require.NoError(t, s.SetSIWCAlphaSearchCompatibility(ctx, true))
	for i := 0; i < 100; i++ {
		enabled, err = s.GetSIWCAlphaSearchCompatibility(ctx)
		require.NoError(t, err)
		require.True(t, enabled)
	}
	require.Equal(t, 1, r.getValueCalls, "cached requests must not read DB")
	enabled, err = newPanelRateLimitTestService(r).GetSIWCAlphaSearchCompatibility(ctx)
	require.NoError(t, err)
	require.True(t, enabled, "persist across restart")
	r.fail = true
	require.Error(t, s.SetSIWCAlphaSearchCompatibility(ctx, false))
	enabled, err = s.GetSIWCAlphaSearchCompatibility(ctx)
	require.NoError(t, err)
	require.True(t, enabled, "failed save must not alter cache")
	r.getValueErr = errors.New("read unavailable")
	s.siwcAlphaSearchCache.Store(&cachedSIWCAlphaSearch{enabled: true, expires: time.Now().Add(-time.Second)})
	enabled, err = s.GetSIWCAlphaSearchCompatibility(ctx)
	require.Error(t, err)
	require.True(t, enabled, "retain last good setting on transient DB failure")
	r.fail = false
	require.NoError(t, s.SetSIWCAlphaSearchCompatibility(ctx, false))
	enabled, err = s.GetSIWCAlphaSearchCompatibility(ctx)
	require.NoError(t, err)
	require.False(t, enabled)
}

func TestSIWCAlphaSearchConcurrentReadSave(t *testing.T) {
	s := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = s.SetSIWCAlphaSearchCompatibility(context.Background(), i%4 == 0)
			} else {
				_, _ = s.GetSIWCAlphaSearchCompatibility(context.Background())
			}
		}(i)
	}
	wg.Wait()
	require.NoError(t, s.SetSIWCAlphaSearchCompatibility(context.Background(), true))
	got, err := s.GetSIWCAlphaSearchCompatibility(context.Background())
	require.NoError(t, err)
	require.True(t, got)
	persisted, err := newPanelRateLimitTestService(s.settingRepo).GetSIWCAlphaSearchCompatibility(context.Background())
	require.NoError(t, err)
	require.Equal(t, got, persisted)
}
