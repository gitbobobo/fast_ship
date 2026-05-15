package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
)

type AuthHandler struct {
	authService *service.AuthService
	storage     storage.Storage
	cfg         *config.Config
}

func NewAuthHandler(authService *service.AuthService, storage storage.Storage, cfg *config.Config) *AuthHandler {
	return &AuthHandler{authService: authService, storage: storage, cfg: cfg}
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

var allowedAvatarExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
}

const maxAvatarSize = 5 * 1024 * 1024 // 5MB

func (h *AuthHandler) UploadAvatar(c *gin.Context) {
	userID := middleware.GetUserID(c)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		response.BadRequest(c, 40001, "未找到上传文件")
		return
	}
	defer file.Close()

	if header.Size > maxAvatarSize {
		response.BadRequest(c, 40002, "头像文件大小不能超过 5MB")
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedAvatarExts[ext] {
		response.BadRequest(c, 40003, "仅支持 jpg、png、gif、webp 格式的图片")
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		response.InternalError(c, "读取文件失败")
		return
	}

	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		response.BadRequest(c, 40004, "文件内容不是有效的图片")
		return
	}

	// 删除旧头像
	user, err := h.authService.GetMe(userID)
	if err == nil && user.AvatarURL != "" {
		oldPath := strings.TrimPrefix(user.AvatarURL, "/api/avatars/")
		_ = h.storage.Delete(oldPath)
	}

	filename := uuid.New().String() + ext
	storagePath := fmt.Sprintf("avatars/%s/%s", userID, filename)

	if err := h.storage.Save(storagePath, bytes.NewReader(data)); err != nil {
		response.InternalError(c, "保存头像失败")
		return
	}

	avatarURL := fmt.Sprintf("/api/avatars/%s/%s", userID, filename)
	result, err := h.authService.UpdateAvatar(userID, avatarURL)
	if err != nil {
		_ = h.storage.Delete(storagePath)
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AuthHandler) GetAvatar(c *gin.Context) {
	userID := c.Param("uid")
	filename := c.Param("filename")

	if strings.Contains(userID, "..") || strings.Contains(filename, "..") ||
		strings.Contains(userID, "/") || strings.Contains(filename, "/") {
		response.BadRequest(c, 40005, "非法参数")
		return
	}

	storagePath := fmt.Sprintf("avatars/%s/%s", userID, filename)
	reader, err := h.storage.Get(storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			response.NotFound(c, 40401, "头像不存在")
			return
		}
		response.InternalError(c, "读取头像失败")
		return
	}
	defer reader.Close()

	ext := strings.ToLower(filepath.Ext(filename))
	contentType := "application/octet-stream"
	switch ext {
	case ".jpg", ".jpeg":
		contentType = "image/jpeg"
	case ".png":
		contentType = "image/png"
	case ".gif":
		contentType = "image/gif"
	case ".webp":
		contentType = "image/webp"
	}

	c.Header("Cache-Control", "private, max-age=3600")
	c.DataFromReader(200, -1, contentType, reader, nil)
}
