package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// IntegrationHandler 集成配置（INT-119）：用户级外部服务连接（当前只有
// GitHub App），仅 JWT，不向 API Key 开放；每个用户只读写自己名下的配置。
type IntegrationHandler struct {
	svc *service.IntegrationService
}

func NewIntegrationHandler(svc *service.IntegrationService) *IntegrationHandler {
	return &IntegrationHandler{svc: svc}
}

// Get GET /api/integrations/github-app
func (h *IntegrationHandler) Get(c *gin.Context) {
	out, err := h.svc.Get(c.Request.Context(), middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Update PUT /api/integrations/github-app
func (h *IntegrationHandler) Update(c *gin.Context) {
	var req api.UpdateGitHubAppIntegrationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.svc.Update(c.Request.Context(), middleware.GetUserID(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// Test POST /api/integrations/github-app/test
func (h *IntegrationHandler) Test(c *gin.Context) {
	out, err := h.svc.Test(c.Request.Context(), middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}
