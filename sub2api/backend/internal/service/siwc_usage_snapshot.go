package service

import (
	"context"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

type accountWindowStatsRangeReader interface {
	GetAccountWindowStatsRange(context.Context, int64, time.Time, time.Time) (*usagestats.AccountStats, error)
}

// Read only the SIWC snapshot. Never manufacture missing windows or probe Codex.
func (s *AccountUsageService) getSIWCUsage(ctx context.Context, account *Account, now time.Time) *UsageInfo {
	usage := &UsageInfo{}
	var snapshot SIWCUsageSnapshot
	decodeSIWCExtra(account, siwcUsageKey, &snapshot)
	appID := account.GetCredential("siwc_app_id")
	if appID == "" {
		appID = account.GetOpenAISIWCClientID()
	}
	if appID != "" && snapshot.AppID != appID {
		return usage
	}
	usage.Error = snapshot.Error
	if snapshot.RateLimit == nil || snapshot.CheckedAt.IsZero() {
		return usage
	}
	usage.UpdatedAt = &snapshot.CheckedAt
	reader, canReadStats := s.usageLogRepo.(accountWindowStatsRangeReader)
	for _, window := range []*OpenAIRateLimitWindow{snapshot.RateLimit.PrimaryWindow, snapshot.RateLimit.SecondaryWindow} {
		if window == nil || (window.LimitWindowSeconds != 18000 && window.LimitWindowSeconds != 604800) ||
			math.IsNaN(window.UsedPercent) || math.IsInf(window.UsedPercent, 0) || window.UsedPercent < 0 {
			continue
		}
		reset := time.Unix(window.ResetAt, 0)
		start := reset.Add(-time.Duration(window.LimitWindowSeconds) * time.Second)
		if start.After(snapshot.CheckedAt) || !reset.After(snapshot.CheckedAt) {
			continue
		}
		progress := &UsageProgress{Utilization: window.UsedPercent, ResetsAt: &reset}
		// Bound costs at the last successful sample, including after refresh failure.
		if canReadStats {
			if stats, err := reader.GetAccountWindowStatsRange(ctx, account.ID, start, snapshot.CheckedAt); err == nil {
				progress.WindowStats = windowStatsFromAccountStats(stats)
			} else if usage.Error == "" {
				usage.Error = "窗口费用统计读取失败"
			}
		}
		if window.LimitWindowSeconds == 18000 {
			usage.FiveHour = progress
		} else {
			usage.SevenDay = progress
		}
		if !reset.After(now) && usage.Error == "" {
			usage.Error = "额度窗口已到期，等待刷新"
		}
	}
	return usage
}
