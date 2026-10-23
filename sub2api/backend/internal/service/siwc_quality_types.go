package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type QualityQuestion struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Prompt        string `json:"prompt"`
	Answer        string `json:"answer"`
	MatchMode     string `json:"match_mode"`
	MaxDurationMS int64  `json:"max_duration_ms"`
}
type AccountQualitySettings struct {
	Enabled                   bool              `json:"enabled"`
	AllGroups                 bool              `json:"all_groups"`
	GroupIDs                  []int64           `json:"group_ids"`
	Revision                  string            `json:"revision"`
	UpdatedAt                 time.Time         `json:"updated_at"`
	QuestionEnabled           bool              `json:"question_enabled"`
	ModelAuditEnabled         bool              `json:"model_audit_enabled"`
	DegradationMode           string            `json:"degradation_mode"`
	DegradationConditions     []string          `json:"degradation_conditions"`
	PauseOnDegradation        bool              `json:"pause_on_degradation"`
	SwitchGroupOnDegradation  bool              `json:"switch_group_on_degradation"`
	DegradationGroupID        int64             `json:"degradation_group_id"`
	Questions                 []QualityQuestion `json:"questions"`
	Model                     string            `json:"model"`
	ModelAuditModel           string            `json:"model_audit_model"`
	ReasoningEffort           string            `json:"reasoning_effort"`
	Mode                      string            `json:"mode"`
	IntervalSeconds           int               `json:"interval_seconds"`
	ModelAuditIntervalSeconds int               `json:"model_audit_interval_seconds"`
	RetrySeconds              int               `json:"retry_seconds"`
	FailureLimit              int               `json:"failure_limit"`
	RecoveryLimit             int               `json:"recovery_limit"`
	Concurrency               int               `json:"concurrency"`
	TimeoutSeconds            int               `json:"timeout_seconds"`
	HistoryLimit              int               `json:"history_limit"`
}

func DefaultAccountQualitySettings() AccountQualitySettings {
	return AccountQualitySettings{AllGroups: true, GroupIDs: []int64{}, QuestionEnabled: true, Model: "gpt-6-astra", ModelAuditModel: "gpt-6-astra", ReasoningEffort: "xhigh", Mode: "content_time", IntervalSeconds: 300, ModelAuditIntervalSeconds: 60, RetrySeconds: 60, FailureLimit: 2, RecoveryLimit: 2, Concurrency: 4, TimeoutSeconds: 180, HistoryLimit: 200, Questions: []QualityQuestion{
		{ID: "candy", Name: "糖果配对", Enabled: true, Prompt: "直接回答问题，不允许联网搜索、调用命令或代码。糖果数量为：圆形苹果7、桃子9、西瓜8；五角星苹果7、桃子6、西瓜4。形状可用手感分辨，口味未知。可以事先决定分别摸几个圆形和几个五角星，不放回取出。至少总共取多少颗，才能保证有圆苹果与星桃子，或圆桃子与星苹果的一组？只回复数字。", Answer: "21", MatchMode: "answer", MaxDurationMS: 20000},
		{ID: "clock", Name: "时钟夹角", Enabled: true, Prompt: "连续走动的指针式时钟在3点15分时，时针与分针较小夹角是多少度？只回复数字，不带单位。", Answer: "7.5", MatchMode: "answer", MaxDurationMS: 20000},
		{ID: "percent", Name: "百分比变化", Enabled: true, Prompt: "一个数先增加20%，再在增加后的数值基础上减少20%，最终是原数的百分之多少？只回复数字，不带百分号。", Answer: "96", MatchMode: "answer", MaxDurationMS: 20000},
	}}
}
func (q AccountQualitySettings) ActiveQuestions() []QualityQuestion {
	out := []QualityQuestion{}
	for _, v := range q.Questions {
		if v.Enabled {
			out = append(out, v)
		}
	}
	return out
}
func (q AccountQualitySettings) Validate() error {
	if err := validateQualityOverallPolicy(q); err != nil {
		return err
	}
	if q.Enabled && q.SwitchGroupOnDegradation && q.DegradationGroupID < 1 {
		return errors.New("请选择综合降智后切换的目标分组")
	}
	if !q.AllGroups && len(q.GroupIDs) == 0 {
		return errors.New("请选择至少一个定时检测分组，或选择全部分组")
	}
	if len(q.GroupIDs) > 1000 {
		return errors.New("定时检测分组最多选择1000个")
	}
	groups := map[int64]bool{}
	for _, id := range q.GroupIDs {
		if id < 1 || groups[id] {
			return errors.New("定时检测分组ID必须为不重复的正整数")
		}
		groups[id] = true
	}
	if q.Enabled && !q.QuestionEnabled && !q.ModelAuditEnabled {
		return errors.New("请至少启用一种检测")
	}
	if len(q.Questions) > 50 || (q.QuestionEnabled && len(q.ActiveQuestions()) == 0) {
		return errors.New("请启用至少一题，题库最多50题")
	}
	if q.Mode != "content" && q.Mode != "time" && q.Mode != "content_time" {
		return errors.New("判断方式无效")
	}
	if q.ReasoningEffort != "low" && q.ReasoningEffort != "medium" && q.ReasoningEffort != "high" && q.ReasoningEffort != "xhigh" {
		return errors.New("推理强度无效")
	}
	for _, v := range []string{q.Model, q.ModelAuditModel} {
		if strings.TrimSpace(v) == "" || len(v) > 200 {
			return errors.New("模型名称无效")
		}
	}
	for _, n := range []int{q.IntervalSeconds, q.ModelAuditIntervalSeconds, q.RetrySeconds} {
		if n < 10 || n > 86400 {
			return errors.New("间隔必须为10至86400秒")
		}
	}
	if q.FailureLimit < 1 || q.FailureLimit > 20 || q.RecoveryLimit < 1 || q.RecoveryLimit > 20 || q.Concurrency < 1 || q.TimeoutSeconds < 5 || q.TimeoutSeconds > 300 || q.HistoryLimit < 1 || q.HistoryLimit > 1000 {
		return errors.New("连续次数、并发、超时或记录上限无效")
	}
	seen := map[string]bool{}
	for _, v := range q.Questions {
		if v.ID == "" || len(v.ID) > 80 || seen[v.ID] || strings.TrimSpace(v.Name) == "" || len(v.Name) > 200 {
			return errors.New("题目需要唯一ID和名称")
		}
		seen[v.ID] = true
		if strings.TrimSpace(v.Prompt) == "" || len(v.Prompt) > 15000 || len(v.Answer) > 2000 || (q.Mode != "time" && strings.TrimSpace(v.Answer) == "") || v.MaxDurationMS < 1 || v.MaxDurationMS > 300000 {
			return errors.New("题目内容、答案或耗时阈值无效")
		}
		switch v.MatchMode {
		case "answer", "keyword":
		case "regex":
			if _, err := regexp.Compile(v.Answer); err != nil {
				return fmt.Errorf("答案正则无效: %w", err)
			}
		default:
			return errors.New("答案匹配方式无效")
		}
	}
	return nil
}

type QualityVerdict struct {
	Status          string     `json:"status"`
	Degraded        bool       `json:"degraded"`
	Failures        int        `json:"failures"`
	Successes       int        `json:"successes"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	EvidenceAt      *time.Time `json:"evidence_at,omitempty"`
	StreakStartedAt *time.Time `json:"streak_started_at,omitempty"`
	NextAt          *time.Time `json:"next_at,omitempty"`
	Error           string     `json:"error,omitempty"`
}
type QualityQuestionResult struct {
	QualityVerdict
	QuestionID     string `json:"question_id,omitempty"`
	QuestionName   string `json:"question_name,omitempty"`
	NextQuestionID string `json:"next_question_id,omitempty"`
	Answer         string `json:"answer,omitempty"`
	DurationMS     int64  `json:"duration_ms"`
	ContentPassed  bool   `json:"content_passed"`
	TimePassed     bool   `json:"time_passed"`
	Reason         string `json:"reason,omitempty"`
}
type QualityModelResult struct {
	QualityVerdict
	UpstreamEndpoint       string     `json:"upstream_endpoint,omitempty"`
	LatestID               int64      `json:"latest_id"`
	SentModel              string     `json:"sent_model,omitempty"`
	ResponseModel          string     `json:"response_model,omitempty"`
	NoNewSamples           bool       `json:"no_new_samples"`
	StateVersion           string     `json:"state_version,omitempty"`
	StateCollectedAt       *time.Time `json:"state_collected_at,omitempty"`
	StateValidationPending bool       `json:"state_validation_pending,omitempty"`
}
type AccountQualityResult struct {
	stateRefreshPending bool
	DetectionKind       string                `json:"detection_kind,omitempty"`
	RecordedAt          *time.Time            `json:"recorded_at,omitempty"`
	QuestionExecution   string                `json:"question_execution,omitempty"`
	ModelExecution      string                `json:"model_execution,omitempty"`
	AccountID           int64                 `json:"account_id"`
	Revision            string                `json:"revision"`
	Version             string                `json:"version"`
	Question            QualityQuestionResult `json:"question"`
	Model               QualityModelResult    `json:"model"`
	Overall             QualityOverallVerdict `json:"overall"`
	Scheduling          QualityScheduling     `json:"scheduling"`
}

type QualityScheduling struct {
	Paused        bool              `json:"paused"`
	StateRequired bool              `json:"state_required,omitempty"`
	QualityPaused bool              `json:"quality_paused,omitempty"`
	Since         *time.Time        `json:"since,omitempty"`
	NextAt        *time.Time        `json:"next_at,omitempty"`
	Successes     int               `json:"successes"`
	Error         string            `json:"error,omitempty"`
	StateVersions map[string]string `json:"state_versions,omitempty"`
}

var qualityFinalAnswerPattern = regexp.MustCompile(`(?m)^\s*FINAL_ANSWER\s*=\s*([^\r\n]+)\s*$`)
var qualityModelSuffix = regexp.MustCompile(`(?i)(-latest|-\d{4}-\d{2}-\d{2}|-\d{8})$`)

func qualityAnswerMatch(q QualityQuestion, answer string) bool {
	switch q.MatchMode {
	case "answer":
		matches := qualityFinalAnswerPattern.FindAllStringSubmatch(strings.TrimSpace(answer), -1)
		return (len(matches) == 1 && strings.TrimSpace(matches[0][1]) == strings.TrimSpace(q.Answer)) || (len(matches) == 0 && strings.TrimSpace(answer) == strings.TrimSpace(q.Answer))
	case "keyword":
		return strings.Contains(answer, q.Answer)
	case "regex":
		re, err := regexp.Compile(q.Answer)
		return err == nil && re.MatchString(answer)
	}
	return false
}
func applyQualityVerdict(v *QualityVerdict, passed bool, q AccountQualitySettings, at time.Time) {
	v.Error = ""
	v.EvidenceAt = &at
	if passed {
		if v.Successes == 0 {
			v.StreakStartedAt = &at
		}
		v.Failures = 0
		v.Successes++
		v.Status = "normal"
		if v.Successes >= q.RecoveryLimit {
			v.Degraded = false
		}
	} else {
		if v.Failures == 0 {
			v.StreakStartedAt = &at
		}
		v.Successes = 0
		v.Failures++
		v.Status = "suspect"
		if v.Failures >= q.FailureLimit {
			v.Degraded = true
			v.Status = "degraded"
		}
	}
}
func applyQualityAnswer(v *QualityQuestionResult, q AccountQualitySettings, question QualityQuestion, answer string, duration int64, err error, started, now time.Time) {
	v.CheckedAt = &now
	next := now.Add(time.Duration(q.IntervalSeconds) * time.Second)
	v.NextAt = &next
	v.QuestionID = question.ID
	v.QuestionName = question.Name
	v.DurationMS = duration
	if err != nil {
		v.Status = "error"
		v.Error = err.Error()
		next = now.Add(time.Duration(q.RetrySeconds) * time.Second)
		v.NextAt = &next
		return
	}
	v.Answer = answer
	v.ContentPassed = qualityAnswerMatch(question, answer)
	v.TimePassed = duration >= 0 && duration < question.MaxDurationMS
	passed := v.ContentPassed
	if q.Mode == "time" {
		passed = v.TimePassed
	}
	if q.Mode == "content_time" {
		passed = v.ContentPassed && v.TimePassed
	}
	applyQualityVerdict(&v.QualityVerdict, passed, q, started)
	v.Reason = fmt.Sprintf("内容匹配=%t，耗时=%dms，阈值=<%dms", v.ContentPassed, duration, question.MaxDurationMS)
	if !passed {
		next = now.Add(time.Duration(q.RetrySeconds) * time.Second)
		v.NextAt = &next
	}
	active := q.ActiveQuestions()
	for i, item := range active {
		if item.ID == question.ID {
			v.NextQuestionID = active[(i+1)%len(active)].ID
			break
		}
	}
}
