package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type SIWCConfigurationUpdateHandler struct{ settings *service.SettingService }

func NewSIWCConfigurationUpdateHandler(settings *service.SettingService) *SIWCConfigurationUpdateHandler {
	return &SIWCConfigurationUpdateHandler{settings: settings}
}

func (h *SIWCConfigurationUpdateHandler) Get(c *gin.Context) {
	enabled, err := h.settings.GetSIWCConfigurationUpdateCompatibility(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取 SIWC 配置更新兼容设置失败")
		return
	}
	response.Success(c, gin.H{"enabled": enabled})
}

func (h *SIWCConfigurationUpdateHandler) Save(c *gin.Context) {
	var in struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if c.ShouldBindJSON(&in) != nil || in.Enabled == nil {
		response.BadRequest(c, "enabled 必须为布尔值")
		return
	}
	if err := h.settings.SetSIWCConfigurationUpdateCompatibility(c.Request.Context(), *in.Enabled); err != nil {
		response.InternalError(c, "保存 SIWC 配置更新兼容设置失败")
		return
	}
	response.Success(c, gin.H{"enabled": *in.Enabled})
}
