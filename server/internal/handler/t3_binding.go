package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// T3BindingHandler 项目 ↔ T3 项目绑定与 T3 项目发现（INT-89）。
// 仅有的对 API Key 开放的 T3 面：不含凭证、不含线程内容；路由以
// RequireAuth 挂载（JWT / API Key 均可），其余 T3 端点仍仅 JWT。
type T3BindingHandler struct {
	bindingService *service.T3BindingService
}

func NewT3BindingHandler(bindingService *service.T3BindingService) *T3BindingHandler {
	return &T3BindingHandler{bindingService: bindingService}
}

// ListEnvironmentProjects GET /api/environments/t3-projects
func (h *T3BindingHandler) ListEnvironmentProjects(c *gin.Context) {
	userID := middleware.GetUserID(c)
	items, err := h.bindingService.DiscoverProjects(c.Request.Context(), userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// ListBindings GET /api/projects/:id/t3-bindings
func (h *T3BindingHandler) ListBindings(c *gin.Context) {
	userID := middleware.GetUserID(c)
	items, err := h.bindingService.List(c.Request.Context(), userID, c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// UpsertBinding PUT /api/projects/:id/t3-bindings/:environmentId
func (h *T3BindingHandler) UpsertBinding(c *gin.Context) {
	var req service.UpsertT3BindingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	binding, err := h.bindingService.Upsert(c.Request.Context(), userID, c.Param("id"), c.Param("environmentId"), req.T3ProjectId)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, binding)
}

// DeleteBinding DELETE /api/projects/:id/t3-bindings/:environmentId
func (h *T3BindingHandler) DeleteBinding(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.bindingService.Delete(c.Request.Context(), userID, c.Param("id"), c.Param("environmentId")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}
