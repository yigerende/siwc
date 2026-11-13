package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

// Registered only under the existing administrator authentication middleware.
func (h *AccountHandler) SaveSpaceWorkflow(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "无效的账号 ID")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input service.SpaceWorkflow
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "无效的 Space 标记")
		return
	}
	if err = input.Validate(); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	svc, ok := h.adminService.(service.SpaceWorkflowAdmin)
	if !ok {
		response.InternalError(c, "Space 标记暂不可用")
		return
	}
	result, err := svc.SaveSpaceWorkflow(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
