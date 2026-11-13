package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeySpaceWorkflowCleanup = "space_workflow_cleanup_v1"

type SpaceWorkflowCleanupSettings struct {
	Enabled         bool `json:"enabled"`
	RetainMinutes   int  `json:"retain_minutes"`
	IntervalMinutes int  `json:"interval_minutes"`
}

func DefaultSpaceWorkflowCleanupSettings() SpaceWorkflowCleanupSettings {
	return SpaceWorkflowCleanupSettings{RetainMinutes: 60, IntervalMinutes: 5}
}
func (v SpaceWorkflowCleanupSettings) Validate() error {
	if v.RetainMinutes < 0 || v.RetainMinutes > 43200 || v.IntervalMinutes < 1 || v.IntervalMinutes > 1440 {
		return infraerrors.BadRequest("INVALID_SPACE_CLEANUP", "保留时间须为 0～43200 分钟，清理间隔须为 1～1440 分钟")
	}
	return nil
}
func (s *SettingService) GetSpaceWorkflowCleanup(ctx context.Context) (SpaceWorkflowCleanupSettings, error) {
	v := DefaultSpaceWorkflowCleanupSettings()
	raw, e := s.settingRepo.GetValue(ctx, SettingKeySpaceWorkflowCleanup)
	if errors.Is(e, ErrSettingNotFound) {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	if raw != "" {
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			return v, e
		}
	}
	return v, v.Validate()
}
func (s *SettingService) SetSpaceWorkflowCleanup(ctx context.Context, v SpaceWorkflowCleanupSettings) error {
	if e := v.Validate(); e != nil {
		return e
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return s.settingRepo.Set(ctx, SettingKeySpaceWorkflowCleanup, string(raw))
}

type SpaceWorkflowCleanupRepository interface {
	CleanupSpaceWorkflows(context.Context, SpaceWorkflowCleanupSettings, time.Time) (int, error)
}
type SpaceWorkflowCleanup struct {
	settings *SettingService
	repo     SpaceWorkflowCleanupRepository
}

func (s *SpaceWorkflowCleanup) RunDue() {
	if s == nil || s.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, e := s.settings.GetSpaceWorkflowCleanup(ctx)
	if e != nil {
		log.Printf("[SpaceWorkflowCleanup] settings unavailable: %v", e)
		return
	}
	if !cfg.Enabled {
		return
	}
	count, e := s.repo.CleanupSpaceWorkflows(ctx, cfg, time.Now().UTC())
	if e != nil {
		log.Printf("[SpaceWorkflowCleanup] cleanup failed: %v", e)
	} else if count > 0 {
		log.Printf("[SpaceWorkflowCleanup] deleted %d completed SIWC accounts", count)
	}
}
