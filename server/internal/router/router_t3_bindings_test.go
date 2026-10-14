package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
)

// T3 项目发现与绑定端点 —— JWT / API Key 均可（INT-89）：仅有的对 API Key
// 开放的 T3 面（不含凭证、不含线程内容）；其余 T3 端点的 API Key 拒绝覆盖
// 见 router_t3_test.go。

func TestRouterT3BindingEndpointsAuth(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "t3bindjwt", "t3bindjwt@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	jwtAuth := "Bearer " + auth.Token

	rawKey := "T3BINDROUTERKEY123456"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    auth.UserID,
		Name:      "CI-T3BIND",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	apiKeyAuth := "Bearer " + service.FormatApiKey(rawKey)

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
		// API Key 期望结果：可达业务层（非 40301）。
		apiKeyCode     int
		apiKeyBodyCode int // 0 表示只查 HTTP 状态
	}{
		{"discover projects", http.MethodGet, "/api/environments/t3-projects", nil, http.StatusOK, 0},
		{"list bindings", http.MethodGet, "/api/projects/" + project.ID + "/t3-bindings", nil, http.StatusOK, 0},
		{"upsert binding env missing", http.MethodPut, "/api/projects/" + project.ID + "/t3-bindings/no-env", []byte(`{"t3_project_id":"p-1"}`), http.StatusNotFound, errs.ErrT3EnvironmentNotFound.Code},
		{"delete binding idempotent", http.MethodDelete, "/api/projects/" + project.ID + "/t3-bindings/no-env", nil, http.StatusOK, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/anonymous", func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
			}
		})
		t.Run(tc.name+"/api-key", func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", apiKeyAuth)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != tc.apiKeyCode {
				t.Fatalf("expected %d, got %d: %s", tc.apiKeyCode, rec.Code, rec.Body.String())
			}
			if tc.apiKeyBodyCode != 0 {
				var envelope routerEnvelope
				if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("decode envelope: %v", err)
				}
				if envelope.Code != tc.apiKeyBodyCode {
					t.Fatalf("expected code %d, got %d", tc.apiKeyBodyCode, envelope.Code)
				}
			}
		})
		t.Run(tc.name+"/jwt", func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", jwtAuth)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != tc.apiKeyCode {
				t.Fatalf("expected %d, got %d: %s", tc.apiKeyCode, rec.Code, rec.Body.String())
			}
		})
	}
}

// API Key 持有者是另一用户时按项目归属拦截：跨用户一律 40401（不泄露存在性）。
func TestRouterT3BindingOwnership(t *testing.T) {
	env := setupRouterTestEnv(t)
	owner := createRouterTestUser(t, env.db, "user-t3bind-owner", "t3bindowner", "t3bindowner@example.com")
	stranger := createRouterTestUser(t, env.db, "user-t3bind-stranger", "t3bindstranger", "t3bindstranger@example.com")
	project := createRouterTestProject(t, env.db, owner.ID)

	rawKey := "T3BINDSTRANGERKEY12"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    stranger.ID,
		Name:      "CI-stranger",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	strangerAuth := "Bearer " + service.FormatApiKey(rawKey)

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"list", http.MethodGet, "/api/projects/" + project.ID + "/t3-bindings", nil},
		{"upsert", http.MethodPut, "/api/projects/" + project.ID + "/t3-bindings/env-1", []byte(`{"t3_project_id":"p-1"}`)},
		{"delete", http.MethodDelete, "/api/projects/" + project.ID + "/t3-bindings/env-1", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", strangerAuth)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
			var envelope routerEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.Code != errs.ErrProjectNotFound.Code {
				t.Fatalf("expected code %d, got %d", errs.ErrProjectNotFound.Code, envelope.Code)
			}
		})
	}
}
