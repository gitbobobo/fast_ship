package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type ApiKeyHandler struct {
	apiKeyService *service.ApiKeyService
}

func NewApiKeyHandler(apiKeyService *service.ApiKeyService) *ApiKeyHandler {
	return &ApiKeyHandler{apiKeyService: apiKeyService}
}

func (h *ApiKeyHandler) Create(c *gin.Context) {
	var req service.CreateApiKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.apiKeyService.Create(userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *ApiKeyHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	keys, err := h.apiKeyService.List(userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, keys)
}

func (h *ApiKeyHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	if err := h.apiKeyService.Delete(id, userID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}
