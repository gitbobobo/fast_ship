package service

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type authTestServices struct {
	db               *gorm.DB
	cfg              *config.Config
	authService      *AuthService
	jwtBlacklistRepo *repository.JWTBlacklistRepository
	refreshTokenRepo *repository.RefreshTokenRepository
}

func setupAuthTestServices(t *testing.T) *authTestServices {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&model.User{}, &model.JWTBlacklist{}, &model.RefreshToken{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:             "test-secret",
			ExpireHours:        24,
			RefreshExpireHours: 72,
		},
	}

	userRepo := repository.NewUserRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)
	refreshTokenRepo := repository.NewRefreshTokenRepository(db)

	return &authTestServices{
		db:               db,
		cfg:              cfg,
		authService:      NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg, nil),
		jwtBlacklistRepo: jwtBlacklistRepo,
		refreshTokenRepo: refreshTokenRepo,
	}
}

func TestAuthServiceRefreshAccessTokenSuccess(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-refresh-success")

	refreshToken, created, err := services.authService.generateRefreshToken(user)
	if err != nil || !created {
		t.Fatalf("generate refresh token: %v", err)
	}

	result, err := services.authService.RefreshAccessToken(refreshToken)
	if err != nil {
		t.Fatalf("refresh access token: %v", err)
	}
	if result.Token == "" {
		t.Fatalf("expected access token")
	}
	if result.RefreshToken == "" {
		t.Fatalf("expected rotated refresh token")
	}
	if result.RefreshToken == refreshToken {
		t.Fatalf("expected rotated refresh token to differ from original")
	}

	token, err := jwt.Parse(result.Token, func(token *jwt.Token) (any, error) {
		return []byte(services.cfg.JWT.Secret), nil
	})
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("expected jwt.MapClaims, got %T", token.Claims)
	}
	if claims["sub"] != user.ID {
		t.Fatalf("expected sub %q, got %v", user.ID, claims["sub"])
	}

	originalHash := HashApiKey(strings.TrimPrefix(refreshToken, "fsr_"))
	var original model.RefreshToken
	if err := services.db.Where("token_hash = ?", originalHash).First(&original).Error; err != nil {
		t.Fatalf("load original refresh token: %v", err)
	}
	if original.RevokedAt == nil {
		t.Fatalf("expected original refresh token to be revoked")
	}

	rotatedHash := HashApiKey(strings.TrimPrefix(result.RefreshToken, "fsr_"))
	if _, err := services.refreshTokenRepo.FindByHash(rotatedHash); err != nil {
		t.Fatalf("expected rotated refresh token to be persisted: %v", err)
	}
}

func TestAuthServiceRefreshAccessTokenExpiredToken(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-refresh-expired")
	refreshToken := createPersistedRefreshToken(t, services.db, user.ID, time.Now().Add(-time.Hour), nil)

	_, err := services.authService.RefreshAccessToken(refreshToken)
	if err != errs.ErrRefreshTokenInvalid {
		t.Fatalf("expected ErrRefreshTokenInvalid, got %v", err)
	}
}

func TestAuthServiceRefreshAccessTokenRevokedToken(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-refresh-revoked")
	now := time.Now().Add(-time.Minute)
	refreshToken := createPersistedRefreshToken(t, services.db, user.ID, time.Now().Add(time.Hour), &now)

	_, err := services.authService.RefreshAccessToken(refreshToken)
	if err != errs.ErrRefreshTokenInvalid {
		t.Fatalf("expected ErrRefreshTokenInvalid, got %v", err)
	}
}

func TestAuthServiceRefreshAccessTokenInvalidToken(t *testing.T) {
	services := setupAuthTestServices(t)

	_, err := services.authService.RefreshAccessToken("fsr_invalid-token")
	if err != errs.ErrRefreshTokenInvalid {
		t.Fatalf("expected ErrRefreshTokenInvalid, got %v", err)
	}
}

func TestAuthServiceLogoutRevokesRefreshToken(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-logout-refresh")
	refreshToken, created, err := services.authService.generateRefreshToken(user)
	if err != nil || !created {
		t.Fatalf("generate refresh token: %v", err)
	}

	exp := time.Now().Add(time.Hour)
	if err := services.authService.Logout("jti-logout", exp, refreshToken); err != nil {
		t.Fatalf("logout: %v", err)
	}

	blacklisted, err := services.jwtBlacklistRepo.Exists("jti-logout")
	if err != nil {
		t.Fatalf("check jwt blacklist: %v", err)
	}
	if !blacklisted {
		t.Fatalf("expected jwt to be blacklisted")
	}

	refreshHash := HashApiKey(strings.TrimPrefix(refreshToken, "fsr_"))
	var stored model.RefreshToken
	if err := services.db.Where("token_hash = ?", refreshHash).First(&stored).Error; err != nil {
		t.Fatalf("load refresh token: %v", err)
	}
	if stored.RevokedAt == nil {
		t.Fatalf("expected refresh token to be revoked on logout")
	}
}

func TestAuthServiceUpdatePasswordRevokesOldSessions(t *testing.T) {
	services := setupAuthTestServices(t)
	ctx := context.Background()

	reg, err := services.authService.Register(ctx, &RegisterRequest{
		Username: "pwuser",
		Email:    "pwuser@example.com",
		Password: "OldPass123",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	oldRefresh := reg.RefreshToken
	userID := reg.User.Id

	// 再开一条会话，确认改密把多条 refresh token 一并吊销。
	second, err := services.authService.Login(ctx, &LoginRequest{Login: "pwuser", Password: "OldPass123"})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}

	resp, err := services.authService.UpdatePassword(ctx, userID, &UpdatePasswordRequest{
		OldPassword: "OldPass123",
		NewPassword: "NewPass456",
	})
	if err != nil {
		t.Fatalf("update password: %v", err)
	}
	if resp.Token == "" || resp.RefreshToken == "" {
		t.Fatalf("expected new token pair for current session, got %+v", resp)
	}

	if _, err := services.authService.RefreshAccessToken(oldRefresh); err != errs.ErrRefreshTokenInvalid {
		t.Fatalf("expected old refresh token rejected, got %v", err)
	}
	if _, err := services.authService.RefreshAccessToken(second.RefreshToken); err != errs.ErrRefreshTokenInvalid {
		t.Fatalf("expected other session refresh token rejected, got %v", err)
	}

	// 改密前签发的 access token 版本为 0，已不再是当前版本。
	if current, err := services.authService.IsTokenVersionCurrent(userID, 0); err != nil || current {
		t.Fatalf("expected pre-change token version rejected, current=%v err=%v", current, err)
	}
	if current, err := services.authService.IsTokenVersionCurrent(userID, 1); err != nil || !current {
		t.Fatalf("expected post-change token version accepted, current=%v err=%v", current, err)
	}

	token, err := jwt.Parse(resp.Token, func(token *jwt.Token) (any, error) {
		return []byte(services.cfg.JWT.Secret), nil
	})
	if err != nil {
		t.Fatalf("parse new access token: %v", err)
	}
	claims := token.Claims.(jwt.MapClaims)
	if ver, ok := claims["ver"].(float64); !ok || int64(ver) != 1 {
		t.Fatalf("expected ver=1 in new token, got %v", claims["ver"])
	}

	if _, err := services.authService.RefreshAccessToken(resp.RefreshToken); err != nil {
		t.Fatalf("expected new refresh token usable: %v", err)
	}
	if _, err := services.authService.Login(ctx, &LoginRequest{Login: "pwuser", Password: "NewPass456"}); err != nil {
		t.Fatalf("expected login with new password: %v", err)
	}
	if _, err := services.authService.Login(ctx, &LoginRequest{Login: "pwuser", Password: "OldPass123"}); err != errs.ErrLoginFailed {
		t.Fatalf("expected old password rejected, got %v", err)
	}
}

// Rotate 的顺序是先条件吊销再补发：旧行已被吊销（改密全量吊销/登出）时
// 不得再创建后继，否则改密扫描与新签发之间会漏出存活令牌（INT-95 复审）。
func TestRefreshTokenRepositoryRotateDoesNotIssueAfterRevoke(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-rotate-revoked")

	rt := mustCreateRefreshTokenRecord(t, services.db, user.ID, time.Now().Add(time.Hour), nil)
	if err := services.refreshTokenRepo.Revoke(rt.ID); err != nil {
		t.Fatalf("revoke current token: %v", err)
	}

	next := &model.RefreshToken{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		TokenHash: uuid.NewString(),
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
	}
	rotated, err := services.refreshTokenRepo.Rotate(rt.ID, next)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated {
		t.Fatalf("expected rotate to refuse issuance for a revoked token")
	}
	var count int64
	if err := services.db.Model(&model.RefreshToken{}).Where("id = ?", next.ID).Count(&count).Error; err != nil {
		t.Fatalf("count successor: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no successor row, got %d", count)
	}
}

// 登录/注册签发 refresh token 钉死在验证凭证时读到的 token_version 上：
// 版本已被推进（并发改密）则不落库。
func TestRefreshTokenRepositoryCreateIfTokenVersion(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-create-fence")

	mk := func() *model.RefreshToken {
		return &model.RefreshToken{
			ID:        uuid.NewString(),
			UserID:    user.ID,
			TokenHash: uuid.NewString(),
			ExpiresAt: time.Now().Add(time.Hour),
			CreatedAt: time.Now(),
		}
	}

	created, err := services.refreshTokenRepo.CreateIfTokenVersion(mk(), user.TokenVersion+1)
	if err != nil {
		t.Fatalf("create with stale version: %v", err)
	}
	if created {
		t.Fatalf("expected creation skipped when token_version moved on")
	}

	created, err = services.refreshTokenRepo.CreateIfTokenVersion(mk(), user.TokenVersion)
	if err != nil || !created {
		t.Fatalf("expected creation at current version: %v", err)
	}
}

func TestRefreshTokenRepositoryCleanExpired(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-clean-refresh")

	expired := mustCreateRefreshTokenRecord(t, services.db, user.ID, time.Now().Add(-time.Hour), nil)
	revokedAt := time.Now().Add(-time.Minute)
	revoked := mustCreateRefreshTokenRecord(t, services.db, user.ID, time.Now().Add(time.Hour), &revokedAt)
	active := mustCreateRefreshTokenRecord(t, services.db, user.ID, time.Now().Add(time.Hour), nil)

	if err := services.refreshTokenRepo.CleanExpired(); err != nil {
		t.Fatalf("clean expired refresh tokens: %v", err)
	}

	if err := services.db.First(&model.RefreshToken{}, "id = ?", expired.ID).Error; err == nil {
		t.Fatalf("expected expired refresh token to be deleted")
	}
	if err := services.db.First(&model.RefreshToken{}, "id = ?", revoked.ID).Error; err == nil {
		t.Fatalf("expected revoked refresh token to be deleted")
	}
	if err := services.db.First(&model.RefreshToken{}, "id = ?", active.ID).Error; err != nil {
		t.Fatalf("expected active refresh token to remain: %v", err)
	}
}

func createPersistedRefreshToken(t *testing.T, db *gorm.DB, userID string, expiresAt time.Time, revokedAt *time.Time) string {
	t.Helper()

	raw, err := GenerateApiKeyRaw()
	if err != nil {
		t.Fatalf("generate raw refresh token: %v", err)
	}

	token := mustCreateRefreshTokenRecord(t, db, userID, expiresAt, revokedAt)
	token.TokenHash = HashApiKey(raw)
	if err := db.Save(token).Error; err != nil {
		t.Fatalf("save refresh token hash: %v", err)
	}

	return "fsr_" + raw
}

func TestLoginFailureDoesNotLogAttemptedLogin(t *testing.T) {
	services := setupAuthTestServices(t)
	var buf bytes.Buffer
	services.authService.logger = slog.New(slog.NewJSONHandler(&buf, nil))

	probe := "secret-username-should-not-leak"
	_, err := services.authService.Login(context.Background(), &LoginRequest{Login: probe, Password: "wrong"})
	if err == nil {
		t.Fatalf("expected login failure")
	}
	if strings.Contains(buf.String(), probe) {
		t.Fatalf("登录失败不应把尝试的登录名写入无归属日志: %s", buf.String())
	}
}

func mustCreateRefreshTokenRecord(t *testing.T, db *gorm.DB, userID string, expiresAt time.Time, revokedAt *time.Time) *model.RefreshToken {
	t.Helper()

	token := &model.RefreshToken{
		ID:        uuid.NewString(),
		UserID:    userID,
		TokenHash: uuid.NewString(),
		ExpiresAt: expiresAt,
		RevokedAt: revokedAt,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create refresh token: %v", err)
	}
	return token
}
