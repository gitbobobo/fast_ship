package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// T3EnvironmentHandler T3 环境连接与只读线程端点（仅 JWT）。
type T3EnvironmentHandler struct {
	envService     *service.T3EnvironmentService
	threadsService *service.T3ThreadsService
}

func NewT3EnvironmentHandler(envService *service.T3EnvironmentService, threadsService *service.T3ThreadsService) *T3EnvironmentHandler {
	return &T3EnvironmentHandler{envService: envService, threadsService: threadsService}
}

// List GET /api/environments
func (h *T3EnvironmentHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	items, err := h.envService.List(c.Request.Context(), userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// Create POST /api/environments
func (h *T3EnvironmentHandler) Create(c *gin.Context) {
	var req service.CreateT3EnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	env, err := h.envService.Create(c.Request.Context(), userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, env)
}

// Update PUT /api/environments/:id
func (h *T3EnvironmentHandler) Update(c *gin.Context) {
	var req service.UpdateT3EnvironmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	userID := middleware.GetUserID(c)
	env, err := h.envService.Update(c.Request.Context(), userID, c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, env)
}

// Delete DELETE /api/environments/:id
func (h *T3EnvironmentHandler) Delete(c *gin.Context) {
	userID := middleware.GetUserID(c)
	if err := h.envService.Delete(c.Request.Context(), userID, c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// ListThreads GET /api/threads
func (h *T3EnvironmentHandler) ListThreads(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.threadsService.List(c.Request.Context(), userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

// GetThread GET /api/environments/:id/threads/:threadId
func (h *T3EnvironmentHandler) GetThread(c *gin.Context) {
	userID := middleware.GetUserID(c)
	detail, err := h.threadsService.Detail(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, detail)
}

// GetThreadItem GET /api/environments/:id/threads/:threadId/items/:itemId
func (h *T3EnvironmentHandler) GetThreadItem(c *gin.Context) {
	userID := middleware.GetUserID(c)
	item, err := h.threadsService.ItemDetail(c.Request.Context(), userID, c.Param("id"), c.Param("threadId"), c.Param("itemId"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, item)
}
