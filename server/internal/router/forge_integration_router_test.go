package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
)

// decodeErrorEnvelope 解错误信封（decodeRouterEnvelope 只收成功信封）。
func decodeErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) routerEnvelope {
	t.Helper()
	var env routerEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, rec.Body.String())
	}
	return env
}

// 代码仓集成路由（INT-166）的权限冒烟：路由已注册（无凭证 401 而非 404）、
// member JWT 40305、API Key 在 RequireJWT 被 40301 拒。管理员正向路径的
// 业务语义在 service 层测试覆盖。
func TestRouterForgeIntegrationsAdminGated(t *testing.T) {
	env := setupRouterTestEnv(t)
	// 首个注册用户 = admin；邀请注册通道拿到 member（默认角色）。
	admin := registerAndLoginRouterUser(t, env.router, "forgeadmin", "forgeadmin@example.com", "Password123")
	member := registerAndLoginInvitedRouterUser(t, env.router, admin.Token, "forgemember", "forgemember@example.com", "Password123")

	// 无凭证：路由存在且挂在 JWT 之后。
	anonReq := httptest.NewRequest(http.MethodGet, "/api/deployment/integrations", nil)
	anonRec := httptest.NewRecorder()
	env.router.ServeHTTP(anonRec, anonReq)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected anonymous 401, got %d: %s", anonRec.Code, anonRec.Body.String())
	}

	// member JWT：路由通，管理员守卫 40305。
	memberReq := httptest.NewRequest(http.MethodGet, "/api/deployment/integrations", nil)
	memberReq.Header.Set("Authorization", "Bearer "+member.Token)
	memberRec := httptest.NewRecorder()
	env.router.ServeHTTP(memberRec, memberReq)
	if memberRec.Code != http.StatusForbidden {
		t.Fatalf("expected member 403, got %d: %s", memberRec.Code, memberRec.Body.String())
	}
	memberEnv := decodeErrorEnvelope(t, memberRec)
	if memberEnv.Code != 40305 {
		t.Fatalf("expected code 40305, got %d", memberEnv.Code)
	}

	// API Key：RequireJWT 直接 40301（API Key 无管理员操作权限）。
	rawKey := "RAWFORGEKEY1234567890"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    member.UserID,
		Name:      "agent",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	keyReq := httptest.NewRequest(http.MethodGet, "/api/deployment/integrations", nil)
	keyReq.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	keyRec := httptest.NewRecorder()
	env.router.ServeHTTP(keyRec, keyReq)
	if keyRec.Code != http.StatusForbidden {
		t.Fatalf("expected api key 403, got %d: %s", keyRec.Code, keyRec.Body.String())
	}
	keyEnv := decodeErrorEnvelope(t, keyRec)
	if keyEnv.Code != 40301 {
		t.Fatalf("expected code 40301, got %d", keyEnv.Code)
	}

	// 管理员 JWT 正向：列表首条为内置 github-default（路由→服务全链贯通）。
	adminReq := httptest.NewRequest(http.MethodGet, "/api/deployment/integrations", nil)
	adminReq.Header.Set("Authorization", "Bearer "+admin.Token)
	adminRec := httptest.NewRecorder()
	env.router.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("expected admin 200, got %d: %s", adminRec.Code, adminRec.Body.String())
	}
	var listData []struct {
		ID      string `json:"id"`
		Builtin bool   `json:"builtin"`
	}
	decodeRouterEnvelope(t, adminRec, &listData)
	if len(listData) != 1 || listData[0].ID != model.ForgeIntegrationBuiltinGitHubID || !listData[0].Builtin {
		t.Fatalf("expected builtin github-default first, got %+v", listData)
	}

	// DELETE 内置集成：管理员也会被业务规则拒绝（40001）。
	delReq := httptest.NewRequest(http.MethodDelete, "/api/deployment/integrations/"+model.ForgeIntegrationBuiltinGitHubID, nil)
	delReq.Header.Set("Authorization", "Bearer "+admin.Token)
	delRec := httptest.NewRecorder()
	env.router.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusBadRequest {
		t.Fatalf("expected delete builtin 400, got %d: %s", delRec.Code, delRec.Body.String())
	}
	delEnv := decodeErrorEnvelope(t, delRec)
	if delEnv.Code != 40001 {
		t.Fatalf("expected code 40001, got %d", delEnv.Code)
	}
}
