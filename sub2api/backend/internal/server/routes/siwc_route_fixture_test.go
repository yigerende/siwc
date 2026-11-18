package routes

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Upstream route-only fixtures omit dependencies required by SIWC registration.
// Supply an account handler and an unavailable settings store: the real monitor
// returns before accessing a database or issuing any background network request.
// SIWC monitor behavior is exercised by its dedicated service tests.
type routeSIWCSettings struct{ service.SettingRepository }

func (*routeSIWCSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, service.ErrSettingNotFound
}
func (*routeSIWCSettings) GetValue(_ context.Context, key string) (string, error) {
	if strings.HasPrefix(key, "admin_compliance_acknowledgement") {
		value, _ := json.Marshal(service.AdminComplianceAcknowledgement{Version: service.AdminComplianceVersion})
		return string(value), nil
	}
	return "", service.ErrSettingNotFound
}
func newRouteSIWCSettings() *service.SettingService {
	return service.NewSettingService(&routeSIWCSettings{}, &config.Config{})
}
func registerAdminRoutesForTest(v1 *gin.RouterGroup, h *handler.Handlers,
	adminAuth middleware.AdminAuthMiddleware, audit middleware.AuditLogMiddleware,
	stepUp middleware.StepUpAuthMiddleware, settings *service.SettingService,
	limiter *middleware.PanelRateLimiter) {
	if h.Admin.Account == nil {
		h.Admin.Account = &adminhandler.AccountHandler{}
	}
	if settings == nil {
		settings = newRouteSIWCSettings()
	}
	RegisterAdminRoutes(v1, h, adminAuth, audit, stepUp, settings, limiter)
}
