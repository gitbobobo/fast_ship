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
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
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

	result, err := h.authService.Register(c.Request.Context(), &req)
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

	result, err := h.authService.Login(c.Request.Context(), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var body api.LogoutJSONBody
	_ = c.ShouldBindJSON(&body)

	// API Key 没有 jti/exp（INT-203）：不进 JWT 黑名单，仅吊销请求体
	// 携带的 refresh token，避免对 nil 做 float64 断言 panic。
	if middleware.GetAuthType(c) == middleware.AuthTypeApiKey {
		if err := h.authService.Logout("", time.Time{}, api.Deref(body.RefreshToken)); err != nil {
			middleware.HandleAppError(c, err)
			return
		}
		response.SuccessEmpty(c)
		return
	}

	// JWT 路径：中间件已用 WithExpirationRequired 在认证层拒绝缺 exp 的令牌
	// （INT-203）；这里 comma-ok 兑底，exp 缺失按无效令牌返回 401，不做无保护断言。
	expValue, ok := c.Get(middleware.ContextKeyExp)
	expFloat, ok := expValue.(float64)
	if !ok {
		response.Unauthorized(c, errs.ErrTokenInvalid.Code, errs.ErrTokenInvalid.Message)
		return
	}

	if err := h.authService.Logout(c.GetString(middleware.ContextKeyJTI), time.Unix(int64(expFloat), 0), api.Deref(body.RefreshToken)); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
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

// updateMeInput 保留基线的 string+omitempty 绑定语义：生成类型字段是
// *string，{"username":""} 会绑定成非 nil 指针触发 min/email 校验返回 400，
// 而基线里显式空串被 omitempty 跳过（200 且字段不变）。NonEmpty 把 "" 折回
// nil，与 service 的 "空串即不提供" 判定等价。timezone 例外：空串是合法的
// 「清空」意图（INT-138），所以用指针区分省略与显式空串。
type updateMeInput struct {
	Username string  `json:"username" binding:"omitempty,min=2,max=50"`
	Email    string  `json:"email" binding:"omitempty,email"`
	Timezone *string `json:"timezone" binding:"omitempty,max=64"`
}

func (h *AuthHandler) UpdateMe(c *gin.Context) {
	var input updateMeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}

	userID := middleware.GetUserID(c)
	result, err := h.authService.UpdateProfile(userID, &service.UpdateProfileRequest{
		Username: api.NonEmpty(input.Username),
		Email:    api.NonEmpty(input.Email),
		Timezone: input.Timezone,
	})
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
	result, err := h.authService.UpdatePassword(c.Request.Context(), userID, &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
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
	if err == nil && user.AvatarUrl != "" {
		oldPath := strings.TrimPrefix(user.AvatarUrl, "/api/avatars/")
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

	contentType := contentTypeForFileName(filename)

	// 头像地址存在用户表里、改不了缓存键，没法像媒体代理那样加版本参数。
	// 注意这只影响新写入的 freshness：部署前缓存的旧响应（没有硬化头）带着
	// 自己的 max-age=3600，会按它继续被复用，真实残余窗口最长 60 分钟——
	// 新值只是把窗口之后的新请求压回分钟级，别当成旧缓存的退役时间。
	// 与媒体代理同一道同源内容不变量：头像同样认 ?token=，不能以同源文档
	// 交付（上传只验扩展名与内容嗅探，多态文件仍可能被当成图片存下）。
	egress.Attachment(c, filename, contentType, -1, reader)
}
