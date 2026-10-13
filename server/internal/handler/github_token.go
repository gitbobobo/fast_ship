package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// GitHubTokenHandler 用户级 GitHub Token 库（仅 JWT）。
type GitHubTokenHandler struct {
	tokenService *service.GitHubTokenService
}

func NewGitHubTokenHandler(tokenService *service.GitHubTokenService) *GitHubTokenHandler {
	return &GitHubTokenHandler{tokenService: tokenService}
}

func (h *GitHubTokenHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)
	tokens, err := h.tokenService.List(userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, tokens)
}

func (h *GitHubTokenHandler) Create(c *gin.Context) {
	var req service.CreateGitHubTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.tokenService.Create(userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GitHubTokenHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req service.UpdateGitHubTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.tokenService.Update(userID, id, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}

func (h *GitHubTokenHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	if err := h.tokenService.Delete(userID, id); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// Detect 手动刷新一次只读检测。
func (h *GitHubTokenHandler) Detect(c *gin.Context) {
	id := c.Param("id")
	userID := middleware.GetUserID(c)

	result, err := h.tokenService.Detect(userID, id)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, result)
}
