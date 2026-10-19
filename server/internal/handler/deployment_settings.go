package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// DeploymentSettingsHandler 部署设置（INT-129）：GET 所有登录用户，
// PUT 仅管理员（路由层 RequireAdmin）。
type DeploymentSettingsHandler struct {
	svc *service.DeploymentSettingsService
}

func NewDeploymentSettingsHandler(svc *service.DeploymentSettingsService) *DeploymentSettingsHandler {
	return &DeploymentSettingsHandler{svc: svc}
}

// Get GET /api/deployment/settings
func (h *DeploymentSettingsHandler) Get(c *gin.Context) {
	out, err := h.svc.Get(c.Request.Context())
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Update PUT /api/deployment/settings
func (h *DeploymentSettingsHandler) Update(c *gin.Context) {
	var req api.UpdateDeploymentSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.svc.Update(c.Request.Context(), req.PublicUrl)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}
