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

type authMiddlewareEnv struct {
	router           *gin.Engine
	cfg              *config.Config
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

	if err := db.AutoMigrate(&model.User{}, &model.ApiKey{}, &model.JWTBlacklist{}); err != nil {
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
	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, cfg)
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
		apiKeyRepo:       apiKeyRepo,
		jwtBlacklistRepo: jwtBlacklistRepo,
	}
}

func (e *authMiddlewareEnv) jwtToken(userID, username, jti string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      userID,
		"username": username,
		"jti":      jti,
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(time.Hour).Unix(),
	})

	signed, err := token.SignedString([]byte(e.cfg.JWT.Secret))
	if err != nil {
		panic(err)
	}
	return signed
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
