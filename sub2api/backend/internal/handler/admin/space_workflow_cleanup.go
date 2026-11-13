package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SpaceWorkflowCleanupHandler struct{ settings *service.SettingService }

func NewSpaceWorkflowCleanupHandler(s *service.SettingService) *SpaceWorkflowCleanupHandler {
	return &SpaceWorkflowCleanupHandler{settings: s}
}
func (h *SpaceWorkflowCleanupHandler) Get(c *gin.Context) {
	v, e := h.settings.GetSpaceWorkflowCleanup(c.Request.Context())
	if e != nil {
		response.InternalError(c, "读取自动删除设置失败")
		return
	}
	response.Success(c, v)
}
func (h *SpaceWorkflowCleanupHandler) Save(c *gin.Context) {
	var in struct {
		Enabled         *bool `json:"enabled"`
		RetainMinutes   *int  `json:"retain_minutes"`
		IntervalMinutes *int  `json:"interval_minutes"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Enabled == nil || in.RetainMinutes == nil || in.IntervalMinutes == nil {
		response.BadRequest(c, "请完整填写自动删除设置")
		return
	}
	v := service.SpaceWorkflowCleanupSettings{Enabled: *in.Enabled, RetainMinutes: *in.RetainMinutes, IntervalMinutes: *in.IntervalMinutes}
	if e := h.settings.SetSpaceWorkflowCleanup(c.Request.Context(), v); e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, v)
}
