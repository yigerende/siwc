package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"testing"
	"time"
)

type siwcWindowStatsRepo struct {
	UsageLogRepository
	starts, ends []time.Time
	fail         bool
}

func (r *siwcWindowStatsRepo) GetAccountWindowStatsRange(_ context.Context, _ int64, start, end time.Time) (*usagestats.AccountStats, error) {
	r.starts = append(r.starts, start)
	r.ends = append(r.ends, end)
	if r.fail {
		return nil, errors.New("unavailable")
	}
	return &usagestats.AccountStats{Requests: 10, Tokens: 2000, Cost: 1.2345, UserCost: 2.5}, nil
}
func TestSIWCUsageWindowStatsStayWithSnapshot(t *testing.T) {
	checked := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	reset := checked.Add(time.Hour)
	repo := &siwcWindowStatsRepo{}
	s := &AccountUsageService{usageLogRepo: repo}
	snapshot := SIWCUsageSnapshot{AppID: "app", CheckedAt: checked, RateLimit: &OpenAIRateLimit{SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 4, LimitWindowSeconds: 604800, ResetAt: reset.Unix()}}}
	a := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc", "siwc_app_id": "app"}, Extra: map[string]any{}}
	set := func() {
		b, _ := json.Marshal(snapshot)
		var value any
		json.Unmarshal(b, &value)
		a.Extra[siwcUsageKey] = value
	}
	for _, failed := range []bool{false, true} {
		if failed {
			snapshot.Error = "HTTP 401"
			snapshot.AttemptedAt = checked.Add(10 * time.Minute)
		}
		set()
		out := s.getSIWCUsage(context.Background(), a, checked.Add(20*time.Minute))
		if out.FiveHour != nil || out.SevenDay == nil || out.SevenDay.WindowStats.Cost != 1.2345 || out.Error != snapshot.Error {
			t.Fatalf("wrong window: %+v", out)
		}
		i := len(repo.ends) - 1
		if !repo.ends[i].Equal(checked) || !repo.starts[i].Equal(reset.Add(-7*24*time.Hour)) {
			t.Fatal("stats mixed with later costs")
		}
	}
	snapshot.Error = ""
	snapshot.CheckedAt = reset.Add(time.Minute)
	snapshot.RateLimit.SecondaryWindow.ResetAt = reset.Add(7 * 24 * time.Hour).Unix()
	snapshot.RateLimit.SecondaryWindow.UsedPercent = 0
	set()
	out := s.getSIWCUsage(context.Background(), a, snapshot.CheckedAt)
	if out.SevenDay.Utilization != 0 || !repo.starts[len(repo.starts)-1].Equal(reset) {
		t.Fatal("new window reused previous costs")
	}
	a.Credentials["siwc_app_id"] = "changed"
	if out = s.getSIWCUsage(context.Background(), a, checked); out.SevenDay != nil {
		t.Fatal("cross-app cache exposed")
	}
}
func TestSIWCUsageMissingAndFailedStats(t *testing.T) {
	now := time.Now()
	repo := &siwcWindowStatsRepo{fail: true}
	s := &AccountUsageService{usageLogRepo: repo}
	a := &Account{ID: 8, Extra: map[string]any{}}
	out := s.getSIWCUsage(context.Background(), a, now)
	if out.FiveHour != nil || out.SevenDay != nil || out.UpdatedAt != nil || len(repo.ends) != 0 {
		t.Fatal("fabricated empty snapshot")
	}
	snap := SIWCUsageSnapshot{CheckedAt: now, RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 1, LimitWindowSeconds: 18000, ResetAt: now.Add(time.Hour).Unix()}}}
	b, _ := json.Marshal(snap)
	var value any
	json.Unmarshal(b, &value)
	a.Extra[siwcUsageKey] = value
	out = s.getSIWCUsage(context.Background(), a, now)
	if out.FiveHour == nil || out.SevenDay != nil || out.FiveHour.WindowStats != nil || out.Error == "" {
		t.Fatal("stats failure became fake zero")
	}
}
