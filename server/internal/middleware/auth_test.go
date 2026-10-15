package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type authTestEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func TestRequireAuth_AllowsJWT(t *testing.T) {
	env := setupAuthMiddlewareTest(t)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+env.jwtToken("user-1", "tester", "jti-jwt"))

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data map[string]string
	decodeAuthEnvelope(t, rec, &data)
	if data["user_id"] != "user-1" {
		t.Fatalf("expected user_id user-1, got %q", data["user_id"])
	}
	if data["auth_type"] != AuthTypeJWT {
		t.Fatalf("expected auth_type jwt, got %q", data["auth_type"])
	}
	if data["username"] != "tester" {
		t.Fatalf("expected username tester, got %q", data["username"])
	}
}

func TestRequireAuth_AllowsAPIKey(t *testing.T) {
	env := setupAuthMiddlewareTest(t)
	rawKey := "RAWAPIKEY1234567890"

	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    "user-1",
		Name:      "CI-Android",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data map[string]string
	decodeAuthEnvelope(t, rec, &data)
	if data["user_id"] != "user-1" {
		t.Fatalf("expected user_id user-1, got %q", data["user_id"])
	}
	if data["auth_type"] != AuthTypeApiKey {
		t.Fatalf("expected auth_type api_key, got %q", data["auth_type"])
	}
	if data["api_key_name"] != "CI-Android" {
		t.Fatalf("expected api_key_name CI-Android, got %q", data["api_key_name"])
	}
}

func TestRequireAuthWithQueryToken_AllowsJWT(t *testing.T) {
	env := setupAuthMiddlewareTest(t)

	req := httptest.NewRequest(http.MethodGet, "/query-token-protected?token="+env.jwtToken("user-1", "tester", "jti-query-jwt"), nil)

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data map[string]string
	decodeAuthEnvelope(t, rec, &data)
	if data["user_id"] != "user-1" {
		t.Fatalf("expected user_id user-1, got %q", data["user_id"])
	}
	if data["auth_type"] != AuthTypeJWT {
		t.Fatalf("expected auth_type jwt, got %q", data["auth_type"])
	}
}

func TestRequireJWT_RejectsAPIKey(t *testing.T) {
	env := setupAuthMiddlewareTest(t)
	rawKey := "RAWAPIKEY1234567890"

	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    "user-1",
		Name:      "CI-Android",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/jwt-only", nil)
	req.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequireAuth_RejectsBlacklistedJWT(t *testing.T) {
	env := setupAuthMiddlewareTest(t)
	token := env.jwtToken("user-1", "tester", "jti-blacklisted")

	if err := env.jwtBlacklistRepo.Add("jti-blacklisted", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("blacklist token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequireAuth_RejectsStaleTokenVersion(t *testing.T) {
	env := setupAuthMiddlewareTest(t)

	// 改密把 token_version 提到 1 后，此前签发（ver=0）及无 ver 的历史 token 都应失效。
	if err := env.db.Model(&model.User{}).Where("id = ?", "user-1").Update("token_version", 1).Error; err != nil {
		t.Fatalf("bump token version: %v", err)
	}

	zero := int64(0)
	for name, token := range map[string]string{
		"ver0":   env.jwtTokenWithVer("user-1", "tester", "jti-stale-ver0", &zero),
		"legacy": env.jwtToken("user-1", "tester", "jti-stale-legacy"),
	} {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)

		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestRequireAuth_AllowsCurrentTokenVersion(t *testing.T) {
	env := setupAuthMiddlewareTest(t)

	if err := env.db.Model(&model.User{}).Where("id = ?", "user-1").Update("token_version", 1).Error; err != nil {
		t.Fatalf("bump token version: %v", err)
	}

	one := int64(1)
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+env.jwtTokenWithVer("user-1", "tester", "jti-current", &one))

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRequireAuth_RejectsTokenForDeletedUser(t *testing.T) {
	env := setupAuthMiddlewareTest(t)

	if err := env.db.Delete(&model.User{}, "id = ?", "user-1").Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+env.jwtToken("user-1", "tester", "jti-deleted-user"))

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

type authMiddlewareEnv struct {
	router           *gin.Engine
	cfg              *config.Config
	db               *gorm.DB
	apiKeyRepo       *repository.ApiKeyRepository
	jwtBlacklistRepo *repository.JWTBlacklistRepository
}

func setupAuthMiddlewareTest(t *testing.T) *authMiddlewareEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&model.User{}, &model.ApiKey{}, &model.JWTBlacklist{}, &model.RefreshToken{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	user := &model.User{
		ID:           "user-1",
		Username:     "tester",
		Email:        "tester@example.com",
		PasswordHash: "hashed",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:      "test-secret",
			ExpireHours: 24,
		},
	}

	userRepo := repository.NewUserRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)
	refreshTokenRepo := repository.NewRefreshTokenRepository(db)
	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg, nil)
	apiKeyRepo := repository.NewApiKeyRepository(db)

	router := gin.New()
	router.GET("/protected", RequireAuth(cfg, apiKeyRepo, authService), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id":      GetUserID(c),
			"auth_type":    GetAuthType(c),
			"username":     GetUserName(c),
			"api_key_name": GetAPIKeyName(c),
		})
	})
	router.GET("/query-token-protected", RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id":      GetUserID(c),
			"auth_type":    GetAuthType(c),
			"username":     GetUserName(c),
			"api_key_name": GetAPIKeyName(c),
		})
	})
	router.GET("/jwt-only", RequireJWT(cfg, authService), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	return &authMiddlewareEnv{
		router:           router,
		cfg:              cfg,
		db:               db,
		apiKeyRepo:       apiKeyRepo,
		jwtBlacklistRepo: jwtBlacklistRepo,
	}
}

func (e *authMiddlewareEnv) jwtToken(userID, username, jti string) string {
	return e.jwtTokenWithVer(userID, username, jti, nil)
}

// jwtTokenWithVer 签一个带 ver claim 的测试 JWT；ver 为 nil 时省略该 claim，
// 对应改密版本机制上线前签发的历史 token。
func (e *authMiddlewareEnv) jwtTokenWithVer(userID, username, jti string, ver *int64) string {
	claims := jwt.MapClaims{
		"sub":      userID,
		"username": username,
		"jti":      jti,
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	if ver != nil {
		claims["ver"] = *ver
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString([]byte(e.cfg.JWT.Secret))
	if err != nil {
		panic(err)
	}
	return signed
}

// 错误码 → HTTP 状态映射必须遵守 OpenAPI 约定：502xx 是上游网关错误（502），
// 其余 50xxx 才是内部错误（500）。
func TestHandleAppError_MapsStatusByCodeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		code       int
		wantStatus int
	}{
		{40001, http.StatusBadRequest},
		{40101, http.StatusUnauthorized},
		{40301, http.StatusForbidden},
		{40401, http.StatusNotFound},
		{40901, http.StatusConflict},
		{41201, http.StatusPreconditionFailed},
		{50000, http.StatusInternalServerError},
		{50200, http.StatusBadGateway},
		{50201, http.StatusBadGateway},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		HandleAppError(c, errs.New(tc.code, "boom"))
		if rec.Code != tc.wantStatus {
			t.Fatalf("code %d: expected HTTP %d, got %d", tc.code, tc.wantStatus, rec.Code)
		}
	}
}

func decodeAuthEnvelope(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()

	var envelope authTestEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err == nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, target); err == nil {
			return
		}
	}

	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
}
