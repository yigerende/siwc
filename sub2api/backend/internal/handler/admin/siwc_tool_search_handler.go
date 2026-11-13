package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SIWCToolSearchHandler struct{ settings *service.SettingService }

func NewSIWCToolSearchHandler(settings *service.SettingService) *SIWCToolSearchHandler {
	return &SIWCToolSearchHandler{settings: settings}
}

func (h *SIWCToolSearchHandler) Get(c *gin.Context) {
	enabled, err := h.settings.GetSIWCToolSearchCompatibility(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取 SIWC 工具检索兼容设置失败")
		return
	}
	response.Success(c, gin.H{"enabled": enabled})
}

func (h *SIWCToolSearchHandler) Save(c *gin.Context) {
	var in struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Enabled == nil {
		response.BadRequest(c, "enabled 必须为布尔值")
		return
	}
	if err := h.settings.SetSIWCToolSearchCompatibility(c.Request.Context(), *in.Enabled); err != nil {
		response.InternalError(c, "保存 SIWC 工具检索兼容设置失败")
		return
	}
	response.Success(c, gin.H{"enabled": *in.Enabled})
}
