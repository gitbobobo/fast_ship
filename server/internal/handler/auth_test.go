package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestAuthHandlerRefreshSuccess(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "handler-refresh-success")
	refreshToken := createHandlerRefreshToken(t, env.db, user.ID, time.Now().Add(time.Hour), nil)

	body := []byte(`{"refresh_token":"` + refreshToken + `"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/auth/refresh", body)

	env.authHandler.Refresh(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var data struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
	}
	envelope := decodeEnvelope(t, rec, &data)
	if envelope.Code != 0 {
		t.Fatalf("expected success envelope, got code=%d body=%s", envelope.Code, rec.Body.String())
	}
	if data.Token == "" {
		t.Fatalf("expected access token")
	}
	if data.RefreshToken == "" {
		t.Fatalf("expected rotated refresh token")
	}
	if data.RefreshToken == refreshToken {
		t.Fatalf("expected rotated refresh token to change")
	}
}

func TestAuthHandlerRefreshInvalidToken(t *testing.T) {
	env := setupHandlerTestEnv(t)
	body := []byte(`{"refresh_token":"fsr_missing"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/auth/refresh", body)

	env.authHandler.Refresh(ctx)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}

	var envelope apiEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Code != 40105 {
		t.Fatalf("expected code 40105, got %d", envelope.Code)
	}
}

func TestAuthHandlerLogoutRevokesRefreshToken(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "handler-logout-revoke")
	refreshToken := createHandlerRefreshToken(t, env.db, user.ID, time.Now().Add(time.Hour), nil)

	body := []byte(`{"refresh_token":"` + refreshToken + `"}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/auth/logout", body)
	ctx.Set(middleware.ContextKeyJTI, "logout-jti")
	ctx.Set(middleware.ContextKeyExp, float64(time.Now().Add(time.Hour).Unix()))

	env.authHandler.Logout(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	refreshHash := service.HashApiKey(refreshToken[len("fsr_"):])
	var token model.RefreshToken
	if err := env.db.Where("token_hash = ?", refreshHash).First(&token).Error; err != nil {
		t.Fatalf("load refresh token: %v", err)
	}
	if token.RevokedAt == nil {
		t.Fatalf("expected refresh token to be revoked")
	}
}

func createHandlerRefreshToken(t *testing.T, db *gorm.DB, userID string, expiresAt time.Time, revokedAt *time.Time) string {
	t.Helper()

	raw, err := service.GenerateApiKeyRaw()
	if err != nil {
		t.Fatalf("generate raw refresh token: %v", err)
	}

	token := &model.RefreshToken{
		ID:        uuid.NewString(),
		UserID:    userID,
		TokenHash: service.HashApiKey(raw),
		ExpiresAt: expiresAt,
		RevokedAt: revokedAt,
		CreatedAt: time.Now().Add(-time.Minute),
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create refresh token: %v", err)
	}

	return "fsr_" + raw
}
