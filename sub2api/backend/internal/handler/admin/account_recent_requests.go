package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var accountRecentRequestSlots = make(chan struct{}, 2)

func (h *UsageHandler) AccountRecentRequests(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input service.AccountRecentRequestsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid account request history body")
		return
	}
	if err := input.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	select {
	case accountRecentRequestSlots <- struct{}{}:
		defer func() { <-accountRecentRequestSlots }()
	default:
		c.Header("Retry-After", "1")
		response.Error(c, http.StatusTooManyRequests, "Account request history busy")
		return
	}
	results, err := h.usageService.LatestAccountRequests(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"accounts": results})
}
