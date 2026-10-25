package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// ForgeIntegrationHandler 部署级代码仓集成（INT-166）：全部端点挂在管理员组
// （路由层 RequireJWT + RequireAdmin）。
type ForgeIntegrationHandler struct {
	svc *service.ForgeIntegrationService
}

func NewForgeIntegrationHandler(svc *service.ForgeIntegrationService) *ForgeIntegrationHandler {
	return &ForgeIntegrationHandler{svc: svc}
}

// List GET /api/deployment/integrations
func (h *ForgeIntegrationHandler) List(c *gin.Context) {
	out, err := h.svc.List(c.Request.Context())
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Create POST /api/deployment/integrations
func (h *ForgeIntegrationHandler) Create(c *gin.Context) {
	var req api.CreateForgeIntegrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.svc.Create(c.Request.Context(), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Get GET /api/deployment/integrations/:id
func (h *ForgeIntegrationHandler) Get(c *gin.Context) {
	out, err := h.svc.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Update PUT /api/deployment/integrations/:id
func (h *ForgeIntegrationHandler) Update(c *gin.Context) {
	var req api.UpdateForgeIntegrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.svc.Update(c.Request.Context(), c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Delete DELETE /api/deployment/integrations/:id
func (h *ForgeIntegrationHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// Test POST /api/deployment/integrations/:id/test
func (h *ForgeIntegrationHandler) Test(c *gin.Context) {
	out, err := h.svc.Test(c.Request.Context(), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}
