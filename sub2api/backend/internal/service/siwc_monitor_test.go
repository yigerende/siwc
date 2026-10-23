package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type siwcUsageCacheRepo struct {
	AccountRepository
	a *Account
}

func (r *siwcUsageCacheRepo) GetByID(context.Context, int64) (*Account, error) { return r.a, nil }
func (r *siwcUsageCacheRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	for k, v := range updates {
		r.a.Extra[k] = v
	}
	return nil
}

func TestSIWCFirstUsageFailureRespectsRetryInterval(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	repo := &siwcUsageCacheRepo{a: &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc", "siwc_app_id": "app", "siwc_web_access_token": "test"}, Extra: map[string]any{}}}
	m := NewSIWCMonitor(&AccountTestService{accountRepo: repo}, nil)
	m.usageURL = server.URL
	first, err := m.Usage(context.Background(), 7, false)
	if err != nil || first.Error == "" {
		t.Fatal("expected retained upstream failure", err)
	}
	second, err := m.Usage(context.Background(), 7, false)
	if err != nil || calls != 1 || !first.AttemptedAt.Equal(second.AttemptedAt) || second.AppID != "app" {
		t.Fatalf("failed initial usage must remain cached: calls=%d err=%v", calls, err)
	}
}

func TestSIWCUsageWindowsAndAmbiguousData(t *testing.T) {
	now := time.Now()
	good := `{"items":[{"id":"app","name":"sub2api","windows":[{"limit_window_seconds":604800,"used_percent":31,"reset_at":1800000000},{"limit_window_seconds":18000,"used_percent":100,"reset_at":1800000000}]}]}`
	out, err := parseSIWCUsage([]byte(good), 42, "", now)
	if err != nil || out.RateLimit.PrimaryWindow.UsedPercent != 100 || out.RateLimit.SecondaryWindow.UsedPercent != 31 || out.RateLimit.Allowed || !out.RateLimit.LimitReached {
		t.Fatalf("bad HAR mapping: %+v %v", out, err)
	}
	for name, raw := range map[string]string{
		"missing percent":  strings.Replace(good, `"used_percent":31,`, "", 1),
		"out of range":     strings.Replace(good, `"used_percent":31`, `"used_percent":101`, 1),
		"missing reset":    strings.Replace(good, `"reset_at":1800000000`, `"reset_at":0`, 1),
		"duplicate window": strings.Replace(good, `604800`, `18000`, 1),
		"missing app":      strings.Replace(good, `"id":"app"`, `"id":""`, 1),
		"other app":        strings.Replace(good, `"sub2api"`, `"other"`, 1),
		"missing windows":  `{"items":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseSIWCUsage([]byte(raw), 42, "", now); e == nil {
				t.Fatal("accepted invalid usage")
			}
		})
	}
	if _, e := parseSIWCUsage([]byte(good), 42, "other", now); e == nil {
		t.Fatal("accepted another app")
	}
	duplicate := strings.Replace(good, `{"items":[`, `{"items":[{"id":"other","name":"sub2api","windows":[]},`, 1)
	if _, e := parseSIWCUsage([]byte(duplicate), 42, "", now); e == nil {
		t.Fatal("ambiguous same-name applications accepted")
	}
	if out, e := parseSIWCUsage([]byte(duplicate), 42, "app", now); e != nil || out.AppID != "app" {
		t.Fatal("client ID must disambiguate same-name apps", e)
	}
}

func TestSIWCProbeRequiresCompletedStream(t *testing.T) {
	good := "data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"
	answer, model, e := parseSIWCProbe(strings.NewReader(good))
	if e != nil || answer != "21" || model != "gpt-6-astra" {
		t.Fatal(answer, model, e)
	}
	for _, raw := range []string{strings.Replace(good, "response.completed", "response.in_progress", 1), good + "data: {\"type\":\"error\"}\n"} {
		if _, _, e := parseSIWCProbe(strings.NewReader(raw)); e == nil {
			t.Fatal("accepted incomplete or failed stream")
		}
	}
	m := NewSIWCMonitor(&AccountTestService{pluginManager: &PluginManager{}}, nil)
	_, _, e = m.requestProbe(context.Background(), &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc"}}, "gpt-6-astra", "test", "high", 1)
	if e == nil {
		t.Fatal("missing SIWC plugin must never fall back to Codex")
	}
}

func TestSIWCSensitiveWebCredentialPreserved(t *testing.T) {
	if !IsSensitiveCredentialKey("siwc_web_access_token") {
		t.Fatal("web credential must be redacted")
	}
	out := MergePreservingSensitiveCreds(map[string]any{"siwc_web_access_token": "private"}, map[string]any{"auth_mode": "siwc"})
	if out["siwc_web_access_token"] != "private" {
		t.Fatal("redacted UI update lost credential")
	}
}

func TestSIWCUsageReaderUsesCachedWindowWithoutCodexProbe(t *testing.T) {
	now := time.Now()
	snapshot := SIWCUsageSnapshot{CheckedAt: now, RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 40, LimitWindowSeconds: 18000, ResetAt: now.Add(time.Hour).Unix()}, SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 12, LimitWindowSeconds: 604800, ResetAt: now.Add(24 * time.Hour).Unix()}}}
	raw, _ := json.Marshal(snapshot)
	var value any
	json.Unmarshal(raw, &value)
	s := &AccountUsageService{}
	a := &Account{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": "siwc"}, Extra: map[string]any{siwcUsageKey: value}}
	usage, e := s.getOpenAIUsage(context.Background(), a, true)
	if e != nil || usage.FiveHour == nil || usage.FiveHour.Utilization != 40 || usage.SevenDay.Utilization != 12 || !usage.UpdatedAt.Equal(now) {
		t.Fatalf("wrong SIWC cached usage: %+v %v", usage, e)
	}
}

func TestSIWCQualityConsecutiveVerdicts(t *testing.T) {
	q := DefaultAccountQualitySettings()
	q.Enabled = true
	q.Revision = "test"
	now := time.Now()
	v := QualityQuestionResult{}
	item := q.Questions[0]
	applyQualityAnswer(&v, q, item, "wrong", 1, nil, now, now)
	if v.Degraded || v.Failures != 1 {
		t.Fatal("first failure must remain suspect")
	}
	applyQualityAnswer(&v, q, item, "wrong", 1, nil, now, now)
	if !v.Degraded || v.Failures != 2 {
		t.Fatal("second failure must confirm degradation")
	}
	for range 2 {
		applyQualityAnswer(&v, q, item, item.Answer, 1, nil, now, now)
	}
	if v.Degraded || v.Failures != 0 || v.Successes != 2 {
		t.Fatal("recovery streak did not clear degradation")
	}
}
