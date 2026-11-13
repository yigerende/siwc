package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cleanupSettingsRepo struct {
	SettingRepository
	raw string
	err error
}

func (r *cleanupSettingsRepo) GetValue(context.Context, string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if r.raw == "" {
		return "", ErrSettingNotFound
	}
	return r.raw, nil
}
func (r *cleanupSettingsRepo) Set(_ context.Context, _ string, value string) error {
	if r.err != nil {
		return r.err
	}
	r.raw = value
	return nil
}

type spaceCleanupRepoStub struct{ calls int }

func (r *spaceCleanupRepoStub) CleanupSpaceWorkflows(context.Context, SpaceWorkflowCleanupSettings, time.Time) (int, error) {
	r.calls++
	return 1, nil
}

func TestSpaceWorkflowCleanupDefaultsValidationAndFailure(t *testing.T) {
	repo := &cleanupSettingsRepo{}
	s := &SettingService{settingRepo: repo}
	ctx := context.Background()
	cfg, e := s.GetSpaceWorkflowCleanup(ctx)
	require.NoError(t, e)
	require.False(t, cfg.Enabled)
	require.Equal(t, 60, cfg.RetainMinutes)
	require.Equal(t, 5, cfg.IntervalMinutes)
	workerRepo := &spaceCleanupRepoStub{}
	worker := &SpaceWorkflowCleanup{settings: s, repo: workerRepo}
	worker.RunDue()
	require.Zero(t, workerRepo.calls)
	for _, v := range []SpaceWorkflowCleanupSettings{{true, -1, 5}, {true, 60, 0}, {true, 43201, 5}, {true, 60, 1441}} {
		require.Error(t, s.SetSpaceWorkflowCleanup(ctx, v))
		require.Empty(t, repo.raw)
	}
	cfg.Enabled = true
	cfg.RetainMinutes = 0
	require.NoError(t, s.SetSpaceWorkflowCleanup(ctx, cfg))
	actual, e := s.GetSpaceWorkflowCleanup(ctx)
	require.NoError(t, e)
	require.Equal(t, cfg, actual)
	worker.RunDue()
	require.Equal(t, 1, workerRepo.calls)
	repo.err = errors.New("read failed")
	worker.RunDue()
	require.Equal(t, 1, workerRepo.calls)
	cfg.Enabled = false
	require.Error(t, s.SetSpaceWorkflowCleanup(ctx, cfg))
	repo.err = nil
	require.NoError(t, s.SetSpaceWorkflowCleanup(ctx, cfg))
	worker.RunDue()
	require.Equal(t, 1, workerRepo.calls)
}
