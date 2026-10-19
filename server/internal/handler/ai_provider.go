package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// AIProviderHandler AI 提供商管理面（INT-133）：全部仅 JWT，按登录用户隔离。
type AIProviderHandler struct {
	providers *service.AIProviderService
}

func NewAIProviderHandler(providers *service.AIProviderService) *AIProviderHandler {
	return &AIProviderHandler{providers: providers}
}

// Catalog 返回内置 models.dev 目录（无用户数据，仅 JWT 鉴权）。
func (h *AIProviderHandler) Catalog(c *gin.Context) {
	response.Success(c, gin.H{"items": h.providers.Catalog()})
}

func (h *AIProviderHandler) List(c *gin.Context) {
	items, err := h.providers.List(middleware.GetUserID(c))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

func (h *AIProviderHandler) Create(c *gin.Context) {
	var req service.CreateAIProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.providers.Create(middleware.GetUserID(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AIProviderHandler) Get(c *gin.Context) {
	result, err := h.providers.Get(middleware.GetUserID(c), c.Param("id"))
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AIProviderHandler) Update(c *gin.Context) {
	var req service.UpdateAIProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.providers.Update(middleware.GetUserID(c), c.Param("id"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *AIProviderHandler) Delete(c *gin.Context) {
	if err := h.providers.Delete(middleware.GetUserID(c), c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

func (h *AIProviderHandler) TestConnection(c *gin.Context) {
	var req service.TestAIProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	result, err := h.providers.Test(&req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}
