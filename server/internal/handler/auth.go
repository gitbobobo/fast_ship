package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

type AuthHandler struct {
	authService *service.AuthService
}

func NewAuthHandler(authService *service.AuthService) *AuthHandler {
	return &AuthHandler{authService: authService}
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req service.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	result, err := h.authService.Register(&req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req service.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	result, err := h.authService.Login(&req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	jti := c.GetString(middleware.ContextKeyJTI)
	expFloat, _ := c.Get(middleware.ContextKeyExp)
	exp := time.Unix(int64(expFloat.(float64)), 0)

	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = c.ShouldBindJSON(&body)

	if err := h.authService.Logout(jti, exp, body.RefreshToken); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var req service.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	result, err := h.authService.RefreshAccessToken(req.RefreshToken)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AuthHandler) GetMe(c *gin.Context) {
	userID := middleware.GetUserID(c)
	result, err := h.authService.GetMe(userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AuthHandler) UpdateMe(c *gin.Context) {
	var req service.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.authService.UpdateProfile(userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AuthHandler) UpdatePassword(c *gin.Context) {
	var req service.UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	if err := h.authService.UpdatePassword(userID, &req); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, nil)
}
