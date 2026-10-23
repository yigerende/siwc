package service

import (
	"errors"
)

type QualityOverallCondition struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type QualityOverallVerdict struct {
	Status     string                    `json:"status"`
	Reason     string                    `json:"reason"`
	Conditions []QualityOverallCondition `json:"conditions"`
}

func (q AccountQualitySettings) overallConditions() []string {
	if q.DegradationConditions != nil {
		return q.DegradationConditions
	}
	conditions := []string{}
	if q.QuestionEnabled {
		conditions = append(conditions, "question")
	}
	if q.ModelAuditEnabled {
		conditions = append(conditions, "model")
	}
	return conditions
}

func normalizeQualityOverallPolicy(q *AccountQualitySettings) {
	if q.DegradationMode == "" {
		q.DegradationMode = "any"
	}
	q.DegradationConditions = append([]string{}, q.overallConditions()...)
}

func validateQualityOverallPolicy(q AccountQualitySettings) error {
	if q.DegradationMode != "" && q.DegradationMode != "any" && q.DegradationMode != "all" {
		return errors.New("综合降智判断须选择任一条件或全部条件")
	}
	conditions := q.overallConditions()
	if q.Enabled && len(conditions) == 0 {
		return errors.New("请选择至少一个综合降智判断条件")
	}
	seen := map[string]bool{}
	for _, condition := range conditions {
		if seen[condition] || (condition != "question" && condition != "model") {
			return errors.New("综合降智判断条件无效或重复")
		}
		if q.Enabled && ((condition == "question" && !q.QuestionEnabled) || (condition == "model" && !q.ModelAuditEnabled)) {
			return errors.New("综合判断选中的检测条件必须启用")
		}
		seen[condition] = true
	}
	return nil
}

func qualityConditionStatus(v QualityVerdict) string {
	if v.CheckedAt == nil || v.Error != "" || v.Status == "error" || v.Status == "no_samples" || v.Status == "" {
		return "pending"
	}
	if v.Degraded {
		return "degraded"
	}
	if v.Status == "normal" || v.Status == "variant" {
		return "normal"
	}
	return "pending"
}

// Unknown evidence is neither success nor failure. ANY/ALL may still reach a
// conclusive result when another condition already determines the outcome.
func evaluateQualityOverall(q AccountQualitySettings, result AccountQualityResult) QualityOverallVerdict {
	out := QualityOverallVerdict{Status: "pending", Reason: "检测证据不足，等待检测", Conditions: []QualityOverallCondition{}}
	if !q.Enabled || result.Revision != q.Revision {
		out.Reason = "检测未启用或结果不属于当前配置"
		return out
	}
	degraded, normal := 0, 0
	for _, kind := range q.overallConditions() {
		status := "pending"
		if kind == "question" && q.QuestionEnabled {
			status = qualityConditionStatus(result.Question.QualityVerdict)
		} else if kind == "model" && q.ModelAuditEnabled {
			status = qualityConditionStatus(result.Model.QualityVerdict)
		}
		out.Conditions = append(out.Conditions, QualityOverallCondition{Kind: kind, Status: status})
		if status == "degraded" {
			degraded++
		} else if status == "normal" {
			normal++
		}
	}
	count := len(out.Conditions)
	if count == 0 {
		return out
	}
	if q.DegradationMode == "all" {
		if degraded == count {
			out.Status, out.Reason = "degraded", "全部勾选条件均已确认异常"
		} else if normal > 0 {
			out.Status, out.Reason = "normal", "存在已确认正常的条件，不满足全部异常规则"
		}
	} else if degraded > 0 {
		out.Status, out.Reason = "degraded", "至少一个勾选条件已确认异常"
	} else if normal == count {
		out.Status, out.Reason = "normal", "全部勾选条件均正常"
	}
	return out
}
