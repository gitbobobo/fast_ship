package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/golang-jwt/jwt/v5"
)

const (
	ContextKeyUserID   = "user_id"
	ContextKeyAuthType = "auth_type"
	ContextKeyJTI      = "jti"
	ContextKeyExp      = "exp"
	ContextKeyUserName = "username"
	ContextKeyAPIKey   = "api_key_name"

	AuthTypeJWT    = "jwt"
	AuthTypeApiKey = "api_key"
)

type tokenExtractor func(*gin.Context) string

// RequireAuth JWT 或 API Key 均可通过
func RequireAuth(cfg *config.Config, apiKeyRepo *repository.ApiKeyRepository, authService *service.AuthService) gin.HandlerFunc {
	return requireAuth(cfg, apiKeyRepo, authService, extractBearerToken)
}

// RequireAuthWithQueryToken 允许从 URL query 中读取 token，适合 img/video 等无法附带 Authorization 头的资源请求。
func RequireAuthWithQueryToken(cfg *config.Config, apiKeyRepo *repository.ApiKeyRepository, authService *service.AuthService, queryParam string) gin.HandlerFunc {
	return requireAuth(cfg, apiKeyRepo, authService, func(c *gin.Context) string {
		if token := extractBearerToken(c); token != "" {
			return token
		}
		return strings.TrimSpace(c.Query(queryParam))
	})
}

func requireAuth(cfg *config.Config, apiKeyRepo *repository.ApiKeyRepository, authService *service.AuthService, extract tokenExtractor) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extract(c)
		if token == "" {
			response.Unauthorized(c, errs.ErrTokenInvalid.Code, "未提供认证信息")
			c.Abort()
			return
		}

		// 根据格式判断是 JWT 还是 API Key
		if strings.HasPrefix(token, "fsk_") {
			handleApiKeyAuth(c, token, apiKeyRepo)
		} else {
			handleJWTAuth(c, token, cfg, authService)
		}
	}
}

// RequireJWT 仅允许 JWT 认证
func RequireJWT(cfg *config.Config, authService *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractBearerToken(c)
		if token == "" {
			response.Unauthorized(c, errs.ErrTokenInvalid.Code, "未提供认证信息")
			c.Abort()
			return
		}

		if strings.HasPrefix(token, "fsk_") {
			response.Forbidden(c, errs.ErrApiKeyForbidden.Code, errs.ErrApiKeyForbidden.Message)
			c.Abort()
			return
		}

		handleJWTAuth(c, token, cfg, authService)
	}
}

func extractBearerToken(c *gin.Context) string {
	auth := c.GetHeader("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func handleJWTAuth(c *gin.Context, tokenString string, cfg *config.Config, authService *service.AuthService) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(cfg.JWT.Secret), nil
	})

	if err != nil || !token.Valid {
		response.Unauthorized(c, errs.ErrTokenInvalid.Code, errs.ErrTokenInvalid.Message)
		c.Abort()
		return
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		response.Unauthorized(c, errs.ErrTokenInvalid.Code, errs.ErrTokenInvalid.Message)
		c.Abort()
		return
	}

	jti, _ := claims["jti"].(string)
	sub, _ := claims["sub"].(string)
	username, _ := claims["username"].(string)
	exp, _ := claims["exp"].(float64)

	// 检查黑名单
	blacklisted, err := authService.IsTokenBlacklisted(jti)
	if err != nil || blacklisted {
		response.Unauthorized(c, errs.ErrTokenBlacklist.Code, errs.ErrTokenBlacklist.Message)
		c.Abort()
		return
	}

	c.Set(ContextKeyUserID, sub)
	c.Set(ContextKeyAuthType, AuthTypeJWT)
	c.Set(ContextKeyJTI, jti)
	c.Set(ContextKeyExp, exp)
	c.Set(ContextKeyUserName, username)
	c.Next()
}

func handleApiKeyAuth(c *gin.Context, token string, apiKeyRepo *repository.ApiKeyRepository) {
	// 去掉 fsk_ 前缀
	raw := strings.TrimPrefix(token, "fsk_")
	keyHash := service.HashApiKey(raw)

	apiKey, err := apiKeyRepo.FindByKeyHash(keyHash)
	if err != nil {
		response.Unauthorized(c, errs.ErrApiKeyInvalid.Code, errs.ErrApiKeyInvalid.Message)
		c.Abort()
		return
	}

	// 更新最后使用时间（异步，不阻塞请求）
	go func() {
		_ = apiKeyRepo.UpdateLastUsed(apiKey.ID)
	}()

	c.Set(ContextKeyUserID, apiKey.UserID)
	c.Set(ContextKeyAuthType, AuthTypeApiKey)
	c.Set(ContextKeyAPIKey, apiKey.Name)
	c.Next()
}

// GetUserID 从上下文获取用户 ID
func GetUserID(c *gin.Context) string {
	return c.GetString(ContextKeyUserID)
}

// GetAuthType 从上下文获取认证类型
func GetAuthType(c *gin.Context) string {
	return c.GetString(ContextKeyAuthType)
}

// IsJWTAuth 检查是否为 JWT 认证
func IsJWTAuth(c *gin.Context) bool {
	return GetAuthType(c) == AuthTypeJWT
}

func GetUserName(c *gin.Context) string {
	return c.GetString(ContextKeyUserName)
}

func GetAPIKeyName(c *gin.Context) string {
	return c.GetString(ContextKeyAPIKey)
}

// HandleAppError 将 AppError 转换为 HTTP 响应
func HandleAppError(c *gin.Context, err error) {
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		httpStatus := http.StatusBadRequest
		switch {
		case appErr.Code >= 40100 && appErr.Code < 40200:
			httpStatus = http.StatusUnauthorized
		case appErr.Code >= 40300 && appErr.Code < 40400:
			httpStatus = http.StatusForbidden
		case appErr.Code >= 40400 && appErr.Code < 40500:
			httpStatus = http.StatusNotFound
		case appErr.Code >= 40900 && appErr.Code < 41000:
			httpStatus = http.StatusConflict
		case appErr.Code >= 50000:
			httpStatus = http.StatusInternalServerError
		}
		response.Error(c, httpStatus, appErr.Code, appErr.Message)
		return
	}
	response.InternalError(c, "服务器内部错误")
}
