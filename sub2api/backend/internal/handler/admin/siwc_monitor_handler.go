package admin

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"strconv"
)

type SIWCMonitorHandler struct{ monitor *service.SIWCMonitor }

func (h *AccountHandler) NewSIWCMonitorHandler(settings *service.SettingService) *SIWCMonitorHandler {
	m := service.NewSIWCMonitor(h.accountTestService, settings)
	m.Start(context.Background())
	return &SIWCMonitorHandler{monitor: m}
}
func (h *SIWCMonitorHandler) Capabilities(c *gin.Context) {
	response.Success(c, gin.H{"version": 1, "max_accounts": 100, "read_only": true, "provider": "siwc"})
}
func (h *SIWCMonitorHandler) Settings(c *gin.Context) {
	q, e := h.monitor.Settings(c.Request.Context())
	if e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	response.Success(c, gin.H{"settings": q})
}
func (h *SIWCMonitorHandler) Save(c *gin.Context) {
	var q service.AccountQualitySettings
	if c.ShouldBindJSON(&q) != nil {
		response.BadRequest(c, "无效配置")
		return
	}
	q, e := h.monitor.SaveSettings(c.Request.Context(), q)
	if e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	response.Success(c, gin.H{"settings": q})
}
func (h *SIWCMonitorHandler) Results(c *gin.Context) {
	var in struct {
		IDs []int64 `json:"account_ids"`
	}
	if c.Param("id") != "" {
		id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
		in.IDs = []int64{id}
	} else if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "账号参数无效")
		return
	}
	if len(in.IDs) < 1 || len(in.IDs) > 100 {
		response.BadRequest(c, "请选择 1～100 个账号")
		return
	}
	v, e := h.monitor.Results(c.Request.Context(), in.IDs)
	if e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	response.Success(c, v)
}
func (h *SIWCMonitorHandler) Run(c *gin.Context) {
	var in struct {
		IDs      []int64 `json:"account_ids"`
		Revision string  `json:"revision"`
	}
	if c.ShouldBindJSON(&in) != nil || len(in.IDs) > 100 {
		response.BadRequest(c, "账号参数无效")
		return
	}
	if c.FullPath() == "/api/v1/admin/account-quality/run-selected" && len(in.IDs) == 0 {
		response.BadRequest(c, "请选择账号")
		return
	}
	if err := h.monitor.Schedule(c.Request.Context(), in.IDs, in.Revision); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"status": "queued", "scheduled": true})
}
func (h *SIWCMonitorHandler) Progress(c *gin.Context) {
	v, e := h.monitor.Progress(c.Request.Context())
	if e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	response.Success(c, v)
}
func (h *SIWCMonitorHandler) History(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	v, e := h.monitor.History(c.Request.Context(), id)
	if e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	response.Success(c, gin.H{"items": v})
}
func (h *SIWCMonitorHandler) Usage(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if id < 1 {
		response.BadRequest(c, "账号 ID 无效")
		return
	}
	age, _ := strconv.Atoi(c.DefaultQuery("max_age_seconds", "120"))
	v, e := h.monitor.Usage(c.Request.Context(), id, c.Request.Method == "POST", age)
	if e != nil {
		response.BadRequest(c, e.Error())
		return
	}
	response.Success(c, v)
}
func (h *SIWCMonitorHandler) BatchUsage(c *gin.Context) {
	var in struct {
		IDs []int64 `json:"account_ids"`
	}
	if c.ShouldBindJSON(&in) != nil || len(in.IDs) < 1 || len(in.IDs) > 100 {
		response.BadRequest(c, "请选择 1～100 个账号")
		return
	}
	out := []service.SIWCUsageSnapshot{}
	for _, id := range in.IDs {
		v, e := h.monitor.Usage(c.Request.Context(), id, false)
		if e != nil {
			v = service.SIWCUsageSnapshot{AccountID: id, Error: e.Error()}
		}
		out = append(out, v)
	}
	response.Success(c, gin.H{"version": 1, "accounts": out})
}
