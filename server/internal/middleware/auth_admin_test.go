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

type adminGuardEnv struct {
	router      *gin.Engine
	db          *gorm.DB
	cfg         *config.Config
	authService *service.AuthService
}

func setupAdminGuardTest(t *testing.T) *adminGuardEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.JWTBlacklist{}, &model.RefreshToken{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	now := time.Now()
	for _, u := range []model.User{
		{ID: "admin-1", Username: "boss", Email: "boss@example.com", PasswordHash: "x", Role: model.UserRoleAdmin, CreatedAt: now, UpdatedAt: now},
		{ID: "member-1", Username: "peer", Email: "peer@example.com", PasswordHash: "x", Role: model.UserRoleMember, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.ID, err)
		}
	}

	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret", ExpireHours: 24}}
	authService := service.NewAuthService(
		repository.NewUserRepository(db),
		repository.NewJWTBlacklistRepository(db),
		repository.NewRefreshTokenRepository(db), cfg, nil)

	router := gin.New()
	router.GET("/admin-only", RequireJWT(cfg, authService), RequireAdmin(authService), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	return &adminGuardEnv{router: router, db: db, cfg: cfg, authService: authService}
}

func (e *adminGuardEnv) sign(userID string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID, "username": userID, "jti": uuid.NewString(),
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(e.cfg.JWT.Secret))
	if err != nil {
		t := &testing.T{}
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

// RequireAdmin 拦截非 admin（40305）；admin 放行；角色改库立即生效（每请求查库）。
func TestRequireAdmin(t *testing.T) {
	env := setupAdminGuardTest(t)

	do := func(userID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin-only", nil)
		req.Header.Set("Authorization", "Bearer "+env.sign(userID))
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec
	}

	// admin → 200。
	if rec := do("admin-1"); rec.Code != http.StatusOK {
		t.Fatalf("expected admin allowed, got %d: %s", rec.Code, rec.Body.String())
	}

	// member → 403 + 40305。
	rec := do("member-1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected member rejected with 403, got %d", rec.Code)
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Code != 40305 {
		t.Fatalf("expected code 40305, got %d", envelope.Code)
	}

	// admin 被降级后立刻失效：不缓存、不写 JWT。
	if err := env.db.Model(&model.User{}).Where("id = ?", "admin-1").
		Update("role", model.UserRoleMember).Error; err != nil {
		t.Fatalf("demote admin: %v", err)
	}
	if rec := do("admin-1"); rec.Code != http.StatusForbidden {
		t.Fatalf("expected demoted admin rejected, got %d", rec.Code)
	}
}
