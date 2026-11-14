package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// stubPublicURL 固定返回值（或空串）的 PublicURLSource 测试桩。
type stubPublicURL struct{ value string }

func (s stubPublicURL) PublicURL(context.Context) string { return s.value }

// setupMCPAuthTest 建最小 OAuthService（空库即可：BaseURL 分支不触达令牌表）。
func setupMCPAuthTest(t *testing.T, publicURL string) *service.OAuthService {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.JWTBlacklist{}, &model.RefreshToken{}, &model.Project{},
		&model.OAuthClient{}, &model.OAuthAuthorization{}, &model.OAuthAuthorizationCode{},
		&model.OAuthAccessToken{}, &model.OAuthRefreshToken{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret"}}
	authService := service.NewAuthService(
		repository.NewUserRepository(db),
		repository.NewJWTBlacklistRepository(db),
		repository.NewRefreshTokenRepository(db),
		cfg, nil,
	)
	return service.NewOAuthService(
		repository.NewOAuthRepository(db),
		repository.NewUserRepository(db),
		repository.NewProjectRepository(db),
		authService,
		service.OAuthServiceOptions{PublicURL: stubPublicURL{value: publicURL}},
	)
}

func runMCPAuthRequest(t *testing.T, oauthService *service.OAuthService, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	r.Any("/mcp", RequireMCPToken(oauthService), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// 未配置 public_url 时 /mcp 与 well-known / token 端点同语义：503
// temporarily_unavailable，无 WWW-Authenticate（写不出绝对发现地址）。
func TestMCPAuthUnavailableWithoutPublicURL(t *testing.T) {
	svc := setupMCPAuthTest(t, "")
	w := runMCPAuthRequest(t, svc, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("WWW-Authenticate should be absent, got %q", w.Header().Get("WWW-Authenticate"))
	}
	if !strings.Contains(w.Body.String(), "temporarily_unavailable") {
		t.Fatalf("body = %q, want temporarily_unavailable", w.Body.String())
	}
}

// 已配置 public_url 时按 RFC 6750 挑战：401 + 绝对 resource_metadata。
func TestMCPAuthChallengeWithPublicURL(t *testing.T) {
	svc := setupMCPAuthTest(t, "https://fs.example.com")
	w := runMCPAuthRequest(t, svc, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	challenge := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="https://fs.example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("challenge = %q, want absolute resource_metadata", challenge)
	}
	if strings.Contains(challenge, "invalid_token") {
		t.Fatalf("challenge = %q, 无令牌不应带 invalid_token", challenge)
	}
}
