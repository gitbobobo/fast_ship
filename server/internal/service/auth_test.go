package service

import (
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
		authService:      NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg),
		jwtBlacklistRepo: jwtBlacklistRepo,
		refreshTokenRepo: refreshTokenRepo,
	}
}

func TestAuthServiceRefreshAccessTokenSuccess(t *testing.T) {
	services := setupAuthTestServices(t)
	user := createTestUser(t, services.db, "user-refresh-success")

	refreshToken, err := services.authService.generateRefreshToken(user.ID)
	if err != nil {
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
	refreshToken, err := services.authService.generateRefreshToken(user.ID)
	if err != nil {
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
