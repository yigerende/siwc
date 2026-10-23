package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/imroc/req/v3"
)

const siwcQualityKey = "siwc_quality_settings_v1"
const siwcUsageKey = "siwc_usage"

type SIWCUsageSnapshot struct {
	AccountID   int64            `json:"account_id"`
	AppID       string           `json:"app_id,omitempty"`
	CheckedAt   time.Time        `json:"checked_at"`
	AttemptedAt time.Time        `json:"attempted_at"`
	Error       string           `json:"error,omitempty"`
	RateLimit   *OpenAIRateLimit `json:"rate_limit,omitempty"`
}
type SIWCMonitor struct {
	test     *AccountTestService
	settings *SettingService
	mu       sync.Mutex
	busy     map[int64]bool
	runMu    sync.Mutex
	queued   bool
	usageURL string
}

func NewSIWCMonitor(test *AccountTestService, settings *SettingService) *SIWCMonitor {
	return &SIWCMonitor{test: test, settings: settings, busy: map[int64]bool{}, usageURL: "https://chatgpt.com/backend-api/wham/usage/chatpass/apps"}
}
func (m *SIWCMonitor) Settings(ctx context.Context) (AccountQualitySettings, error) {
	q := DefaultAccountQualitySettings()
	q.Revision = "siwc-default-v1"
	raw, err := m.settings.settingRepo.GetMultiple(ctx, []string{siwcQualityKey})
	if err != nil {
		return q, err
	}
	if raw[siwcQualityKey] != "" {
		err = json.Unmarshal([]byte(raw[siwcQualityKey]), &q)
	}
	normalizeQualityOverallPolicy(&q)
	return q, err
}
func (m *SIWCMonitor) SaveSettings(ctx context.Context, q AccountQualitySettings) (AccountQualitySettings, error) {
	q.PauseOnDegradation = false
	q.SwitchGroupOnDegradation = false
	normalizeQualityOverallPolicy(&q)
	if err := q.Validate(); err != nil {
		return q, err
	}
	// Routing is owned by Space. No ticket/state refresh machinery is installed.
	q.PauseOnDegradation = false
	q.SwitchGroupOnDegradation = false
	q.Revision = uuid.NewString()
	q.UpdatedAt = time.Now().UTC()
	b, err := json.Marshal(q)
	if err == nil {
		err = m.settings.settingRepo.Set(ctx, siwcQualityKey, string(b))
	}
	return q, err
}
func decodeSIWCExtra(a *Account, key string, out any) {
	b, _ := json.Marshal(a.Extra[key])
	_ = json.Unmarshal(b, out)
}
func (m *SIWCMonitor) Results(ctx context.Context, ids []int64) (map[string]any, error) {
	if len(ids) > 100 {
		return nil, errors.New("每批最多 100 个账号")
	}
	q, err := m.Settings(ctx)
	if err != nil {
		return nil, err
	}
	as, err := m.test.accountRepo.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := []AccountQualityResult{}
	for _, a := range as {
		if !a.IsOpenAISIWC() {
			continue
		}
		v := AccountQualityResult{AccountID: a.ID}
		decodeSIWCExtra(a, "siwc_quality", &v)
		if v.Revision != q.Revision {
			v = AccountQualityResult{AccountID: a.ID, Revision: q.Revision}
		}
		v.Overall = evaluateQualityOverall(q, v)
		out = append(out, v)
	}
	return map[string]any{"version": 1, "server_now": time.Now().UTC(), "settings": q, "accounts": out}, nil
}
func (m *SIWCMonitor) History(ctx context.Context, id int64) ([]AccountQualityResult, error) {
	a, e := m.test.accountRepo.GetByID(ctx, id)
	if e != nil {
		return nil, e
	}
	out := []AccountQualityResult{}
	decodeSIWCExtra(a, "siwc_quality_history", &out)
	return out, nil
}
func (m *SIWCMonitor) claim(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy[id] {
		return false
	}
	m.busy[id] = true
	return true
}
func (m *SIWCMonitor) release(id int64) { m.mu.Lock(); delete(m.busy, id); m.mu.Unlock() }

func (m *SIWCMonitor) Schedule(ctx context.Context, ids []int64, revision string) error {
	if len(ids) > 100 {
		return errors.New("每批最多 100 个账号")
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id < 1 || seen[id] {
			return errors.New("账号 ID 必须为正数且不重复")
		}
		seen[id] = true
	}
	q, e := m.Settings(ctx)
	if e != nil {
		return e
	}
	if revision != "" && revision != q.Revision {
		return errors.New("检测配置已变化，请刷新后重试")
	}
	m.mu.Lock()
	if m.queued {
		m.mu.Unlock()
		return errors.New("已有手动检测等待执行")
	}
	m.queued = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); m.queued = false; m.mu.Unlock() }()
		call, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		// Keep the accepted manual run queued while a scheduled batch finishes.
		_ = m.run(call, ids, true, true)
	}()
	return nil
}

func (m *SIWCMonitor) Progress(ctx context.Context) (map[string]any, error) {
	q, e := m.Settings(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := m.test.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if e != nil {
		return nil, e
	}
	question := map[string]any{"enabled": q.Enabled && q.QuestionEnabled, "total": 0, "checked": 0, "unchecked": 0, "pending": 0}
	model := map[string]any{"enabled": q.Enabled && q.ModelAuditEnabled, "total": 0, "checked": 0, "unchecked": 0, "pending": 0}
	summary := map[string]int{"total": 0, "normal": 0, "degraded": 0, "pending": 0}
	for i := range rows {
		a := &rows[i]
		if !a.IsOpenAISIWC() || (!q.AllGroups && !hasSIWCGroup(a.GroupIDs, q.GroupIDs)) {
			continue
		}
		v := AccountQualityResult{}
		decodeSIWCExtra(a, "siwc_quality", &v)
		summary["total"]++
		status := evaluateQualityOverall(q, v).Status
		summary[status]++
		for j, part := range []QualityVerdict{v.Question.QualityVerdict, v.Model.QualityVerdict} {
			target := question
			if j == 1 {
				target = model
			}
			target["total"] = target["total"].(int) + 1
			key := "unchecked"
			if v.Revision == q.Revision && part.CheckedAt != nil {
				key = "checked"
			}
			target[key] = target[key].(int) + 1
			if part.NextAt == nil || time.Now().After(*part.NextAt) {
				target["pending"] = target["pending"].(int) + 1
			}
		}
	}
	m.mu.Lock()
	running := len(m.busy) > 0
	queued := m.queued
	m.mu.Unlock()
	return map[string]any{"progress": map[string]any{"server_now": time.Now().UTC(), "question": question, "model": model, "running": running, "queued": queued}, "summary": summary}, nil
}
func (m *SIWCMonitor) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = m.Run(ctx, nil, false)
			}
		}
	}()
}
func (m *SIWCMonitor) Run(ctx context.Context, ids []int64, force bool) error {
	return m.run(ctx, ids, force, false)
}
func (m *SIWCMonitor) run(ctx context.Context, ids []int64, force, wait bool) error {
	if wait {
		m.runMu.Lock()
	} else if !m.runMu.TryLock() {
		return errors.New("SIWC 检测正在执行")
	}
	defer m.runMu.Unlock()
	q, err := m.Settings(ctx)
	if err != nil {
		return err
	}
	var as []*Account
	if len(ids) > 0 {
		if len(ids) > 100 {
			return errors.New("每批最多 100 个账号")
		}
		as, err = m.test.accountRepo.GetByIDs(ctx, ids)
	} else {
		var rows []Account
		rows, err = m.test.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
		for i := range rows {
			as = append(as, &rows[i])
		}
	}
	if err != nil {
		return err
	}
	jobs := make(chan *Account)
	var wg sync.WaitGroup
	for i := 0; i < max(1, min(q.Concurrency, 8)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				if !a.IsOpenAISIWC() || a.Status != "active" || (!force && !a.Schedulable) {
					continue
				}
				_, _ = m.Usage(ctx, a.ID, false)
				if (!force && !q.Enabled) || (!q.AllGroups && !hasSIWCGroup(a.GroupIDs, q.GroupIDs)) {
					continue
				}
				if !m.claim(a.ID) {
					continue
				}
				_ = m.probe(ctx, a, q, force)
				m.release(a.ID)
			}
		}()
	}
	for _, a := range as {
		select {
		case jobs <- a:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	return nil
}
func hasSIWCGroup(a, b []int64) bool {
	for _, id := range a {
		if slices.Contains(b, id) {
			return true
		}
	}
	return false
}
func (m *SIWCMonitor) probe(ctx context.Context, a *Account, q AccountQualitySettings, force bool) error {
	v := AccountQualityResult{AccountID: a.ID, Revision: q.Revision}
	decodeSIWCExtra(a, "siwc_quality", &v)
	if v.Revision != q.Revision {
		v = AccountQualityResult{AccountID: a.ID, Revision: q.Revision}
	}
	now := time.Now().UTC()
	questionDue := q.QuestionEnabled && (force || v.Question.NextAt == nil || !now.Before(*v.Question.NextAt))
	modelDue := q.ModelAuditEnabled && (force || v.Model.NextAt == nil || !now.Before(*v.Model.NextAt))
	if !questionDue && !modelDue {
		return nil
	}
	if questionDue {
		items := q.ActiveQuestions()
		if len(items) > 0 {
			item := items[0]
			for _, x := range items {
				if x.ID == v.Question.NextQuestionID {
					item = x
				}
			}
			started := time.Now().UTC()
			answer, _, e := m.requestProbe(ctx, a, q.Model, item.Prompt, q.ReasoningEffort, q.TimeoutSeconds)
			applyQualityAnswer(&v.Question, q, item, answer, time.Since(started).Milliseconds(), e, started, time.Now().UTC())
		}
	}
	if modelDue {
		started := time.Now().UTC()
		_, actual, e := m.requestProbe(ctx, a, q.ModelAuditModel, "Reply with OK.", q.ReasoningEffort, q.TimeoutSeconds)
		end := time.Now().UTC()
		next := end.Add(time.Duration(q.ModelAuditIntervalSeconds) * time.Second)
		v.Model.CheckedAt = &end
		v.Model.NextAt = &next
		v.Model.SentModel = q.ModelAuditModel
		v.Model.ResponseModel = actual
		if e != nil || actual == "" {
			v.Model.Status = "error"
			v.Model.Error = "SIWC 未返回有效模型信息"
			if e != nil {
				v.Model.Error = e.Error()
			}
		} else {
			same := qualityModelSuffix.ReplaceAllString(strings.ToLower(q.ModelAuditModel), "") == qualityModelSuffix.ReplaceAllString(strings.ToLower(actual), "")
			applyQualityVerdict(&v.Model.QualityVerdict, same, q, started)
		}
	}
	current, e := m.Settings(ctx)
	if e != nil {
		return e
	}
	if current.Revision != q.Revision {
		return nil
	}
	v.Version = uuid.NewString()
	v.Overall = evaluateQualityOverall(q, v)
	recorded := time.Now().UTC()
	v.RecordedAt = &recorded
	history := []AccountQualityResult{}
	decodeSIWCExtra(a, "siwc_quality_history", &history)
	history = append([]AccountQualityResult{v}, history...)
	history = history[:min(len(history), q.HistoryLimit)]
	return m.test.accountRepo.UpdateExtra(ctx, a.ID, map[string]any{"siwc_quality": v, "siwc_quality_history": history})
}
func (m *SIWCMonitor) requestProbe(ctx context.Context, a *Account, model, prompt, effort string, timeout int) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if m.test.pluginManager == nil {
		return "", "", errors.New("SIWC 请求插件未启用")
	}
	token := a.GetOpenAIAccessToken()
	if gateway := m.test.openaiGatewayService; gateway != nil && gateway.openAITokenProvider != nil {
		var err error
		token, err = gateway.openAITokenProvider.GetAccessToken(ctx, a)
		if err != nil {
			return "", "", errors.New("SIWC 检测凭证刷新失败")
		}
	}
	body, _ := json.Marshal(map[string]any{"model": model, "input": []any{map[string]any{"role": "user", "content": prompt}}, "stream": true, "store": false, "reasoning": map[string]any{"effort": effort}})
	request, _ := http.NewRequestWithContext(ctx, "POST", chatgptCodexAPIURL, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/event-stream")
	proxy := ""
	if a.Proxy != nil {
		proxy = a.Proxy.URL()
	}
	res, handled, err := m.test.pluginManager.RoundTripOpenAIOAuth(ctx, request, proxy, a)
	if err != nil {
		return "", "", errors.New("SIWC 检测请求失败")
	}
	if !handled || res == nil {
		return "", "", errors.New("SIWC 请求插件未接管该账号")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", "", fmt.Errorf("SIWC 检测 HTTP %d", res.StatusCode)
	}
	return parseSIWCProbe(res.Body)
}
func parseSIWCProbe(body io.Reader) (string, string, error) {
	sc := bufio.NewScanner(io.LimitReader(body, 2<<20))
	sc.Buffer(make([]byte, 4096), 1<<20)
	var out strings.Builder
	actual := ""
	complete := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "[DONE]" {
			continue
		}
		var v struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Model    string `json:"model"`
			Response struct {
				Model string `json:"model"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(raw), &v) != nil {
			continue
		}
		if v.Response.Model != "" {
			actual = v.Response.Model
		}
		if v.Model != "" {
			actual = v.Model
		}
		if v.Type == "response.output_text.delta" {
			out.WriteString(v.Delta)
		}
		if v.Type == "response.completed" {
			complete = true
		}
		if v.Type == "error" || v.Type == "response.failed" {
			return "", actual, errors.New("SIWC 检测响应失败")
		}
	}
	if sc.Err() != nil || !complete {
		return "", actual, errors.New("SIWC 检测响应未完整结束")
	}
	return strings.TrimSpace(out.String()), actual, nil
}

func (m *SIWCMonitor) Usage(ctx context.Context, id int64, force bool, ages ...int) (SIWCUsageSnapshot, error) {
	a, err := m.test.accountRepo.GetByID(ctx, id)
	if err != nil {
		return SIWCUsageSnapshot{}, err
	}
	if !a.IsOpenAISIWC() {
		return SIWCUsageSnapshot{}, errors.New("该账号不是 SIWC")
	}
	old := SIWCUsageSnapshot{AccountID: id}
	decodeSIWCExtra(a, siwcUsageKey, &old)
	appID := a.GetCredential("siwc_app_id")
	if appID == "" {
		appID = a.GetOpenAISIWCClientID()
	}
	if appID != "" && old.AppID != appID {
		old = SIWCUsageSnapshot{AccountID: id}
	}
	age := 120
	if len(ages) > 0 {
		age = max(10, min(ages[0], 300))
	}
	if !force && time.Since(old.AttemptedAt) < time.Duration(age)*time.Second {
		return old, nil
	}
	// Separate lock from quality, allowing read-only cached results during a probe.
	if !m.claim(-id) {
		return old, nil
	}
	defer m.release(-id)
	old.AttemptedAt = time.Now().UTC()
	fresh, e := m.fetchUsage(ctx, a)
	if e != nil {
		old.Error = e.Error()
	} else {
		fresh.AttemptedAt = old.AttemptedAt
		old = fresh
	}
	if err = m.test.accountRepo.UpdateExtra(ctx, id, map[string]any{siwcUsageKey: old}); err != nil {
		return old, err
	}
	return old, nil
}
func (m *SIWCMonitor) fetchUsage(ctx context.Context, a *Account) (SIWCUsageSnapshot, error) {
	out := SIWCUsageSnapshot{AccountID: a.ID}
	token := a.GetCredential("siwc_web_access_token")
	if token == "" {
		return out, errors.New("缺少网页登录凭证，请重新获取 SIWC 授权并同步凭证")
	}
	c := req.C().ImpersonateChrome().SetTimeout(20 * time.Second)
	if a.Proxy != nil {
		c.SetProxyURL(a.Proxy.URL())
	}
	headers := map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json", "Origin": "https://chatgpt.com", "Referer": "https://chatgpt.com/"}
	if id := a.GetCredential("siwc_web_account_id"); id != "" {
		headers["ChatGPT-Account-Id"] = id
	}
	resp, err := c.R().SetContext(ctx).SetHeaders(headers).Get(m.usageURL)
	if err != nil {
		return out, errors.New("SIWC 网页额度请求失败")
	}
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("SIWC 网页额度 HTTP %d；保留上次结果", resp.StatusCode)
	}
	appID := a.GetCredential("siwc_app_id")
	if appID == "" {
		appID = a.GetOpenAISIWCClientID()
	}
	return parseSIWCUsage([]byte(resp.String()), a.ID, appID, time.Now().UTC())
}
func parseSIWCUsage(raw []byte, id int64, appID string, now time.Time) (SIWCUsageSnapshot, error) {
	out := SIWCUsageSnapshot{AccountID: id, CheckedAt: now}
	var p struct {
		Items []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Windows []struct {
				UsedPercent        *float64 `json:"used_percent"`
				LimitWindowSeconds int64    `json:"limit_window_seconds"`
				ResetAt            int64    `json:"reset_at"`
			} `json:"windows"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return out, errors.New("SIWC 额度响应格式无效")
	}
	matches := 0
	for _, app := range p.Items {
		if (appID != "" && app.ID != appID) || (appID == "" && !strings.EqualFold(app.Name, "sub2api")) {
			continue
		}
		matches++
		if app.ID == "" {
			return out, errors.New("SIWC 应用 ID 缺失")
		}
		out.AppID = app.ID
		out.RateLimit = &OpenAIRateLimit{Allowed: true}
		seen := map[int64]bool{}
		for _, w := range app.Windows {
			if w.LimitWindowSeconds != 18000 && w.LimitWindowSeconds != 604800 {
				continue
			}
			if seen[w.LimitWindowSeconds] || w.UsedPercent == nil || math.IsNaN(*w.UsedPercent) || math.IsInf(*w.UsedPercent, 0) || *w.UsedPercent < 0 || *w.UsedPercent > 100 || w.ResetAt <= 0 {
				return out, errors.New("SIWC 额度窗口无效")
			}
			seen[w.LimitWindowSeconds] = true
			copy := OpenAIRateLimitWindow{UsedPercent: *w.UsedPercent, LimitWindowSeconds: w.LimitWindowSeconds, ResetAt: w.ResetAt}
			if w.LimitWindowSeconds == 18000 {
				out.RateLimit.PrimaryWindow = &copy
			} else {
				out.RateLimit.SecondaryWindow = &copy
			}
			if *w.UsedPercent >= 100 {
				out.RateLimit.Allowed = false
				out.RateLimit.LimitReached = true
			}
		}
	}
	if matches != 1 || out.RateLimit == nil || out.RateLimit.PrimaryWindow == nil || out.RateLimit.SecondaryWindow == nil {
		return out, errors.New("未找到唯一的 sub2api 应用及完整 5h/7d 窗口")
	}
	return out, nil
}
