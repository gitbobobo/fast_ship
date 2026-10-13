package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"time"
)

// T3 环境与只读线程端点 —— 仅 JWT；API Key 一律拒绝（INT-82）。

func TestRouterT3EndpointsRejectAnonymousAndAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	user := createRouterTestUser(t, env.db, "user-t3", "t3user", "t3user@example.com")

	rawKey := "T3ROUTERKEY123456789"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		Name:      "CI-T3",
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
	}{
		{"list envs", http.MethodGet, "/api/environments", nil},
		{"create env", http.MethodPost, "/api/environments", []byte(`{"name":"x","pairing_url":"https://a.example.com/#token=t"}`)},
		{"update env", http.MethodPut, "/api/environments/some-id", []byte(`{"name":"y"}`)},
		{"delete env", http.MethodDelete, "/api/environments/some-id", nil},
		{"list threads", http.MethodGet, "/api/threads", nil},
		{"thread detail", http.MethodGet, "/api/environments/some-id/threads/th-1", nil},
		{"thread item detail", http.MethodGet, "/api/environments/some-id/threads/th-1/items/src::item-1", nil},
		// INT-85 写入口：API Key 在解析 body/访问环境/发上游之前一律 40301
		{"composer options", http.MethodGet, "/api/environments/some-id/composer-options", nil},
		{"workspace options", http.MethodGet, "/api/environments/some-id/projects/p-1/workspace-options", nil},
		{"create thread", http.MethodPost, "/api/environments/some-id/threads", []byte(`{"project_id":"p-1","message":{"text":"hi"}}`)},
		{"send message", http.MethodPost, "/api/environments/some-id/threads/th-1/messages", []byte(`{"text":"hi"}`)},
		{"stop thread", http.MethodPost, "/api/environments/some-id/threads/th-1/stop", nil},
		{"resume queue", http.MethodPost, "/api/environments/some-id/threads/th-1/queue/resume", nil},
		{"steer queued", http.MethodPost, "/api/environments/some-id/threads/th-1/queue/run-1/steer", []byte(`{}`)},
		{"cancel queued", http.MethodPost, "/api/environments/some-id/threads/th-1/queue/run-1/cancel", nil},
		{"edit queued", http.MethodPut, "/api/environments/some-id/threads/th-1/queue/run-1", []byte(`{"text":"x"}`)},
		{"reorder queued", http.MethodPost, "/api/environments/some-id/threads/th-1/queue/run-1/reorder", []byte(`{"before_run_id":null}`)},
		{"respond request", http.MethodPost, "/api/environments/some-id/threads/th-1/requests/rr-1/respond", []byte(`{"type":"approval","decision":"accept"}`)},
		{"dismiss request", http.MethodPost, "/api/environments/some-id/threads/th-1/requests/rr-1/dismiss", nil},
		{"answer secret", http.MethodPost, "/api/environments/some-id/threads/th-1/items/th-1::sec-1/secret-answer", []byte(`{"type":"save","secret":"x"}`)},
		{"upload attachment", http.MethodPost, "/api/environments/some-id/attachments", []byte("bytes")},
		{"attachment content", http.MethodGet, "/api/environments/some-id/attachments/att-1/content", nil},
		{"delete attachment", http.MethodDelete, "/api/environments/some-id/attachments/att-1", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/anonymous", func(t *testing.T) {
			var reader *bytes.Reader
			if tc.body != nil {
				reader = bytes.NewReader(tc.body)
			} else {
				reader = bytes.NewReader(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, reader)
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
			var reader *bytes.Reader
			if tc.body != nil {
				reader = bytes.NewReader(tc.body)
			} else {
				reader = bytes.NewReader(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, reader)
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", apiKeyAuth)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
			}
			var envelope routerEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.Code != errs.ErrApiKeyForbidden.Code {
				t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
			}
		})
	}
}

func TestRouterT3EndpointsReachHandlerWithJWT(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "t3-jwt", "t3jwt@example.com", "Password123")
	jwtAuth := "Bearer " + auth.Token

	// JWT 通过认证层进入业务：空列表
	listReq := httptest.NewRequest(http.MethodGet, "/api/environments", nil)
	listReq.Header.Set("Authorization", jwtAuth)
	listRec := httptest.NewRecorder()
	env.router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Items []service.T3EnvironmentResponse `json:"items"`
	}
	decodeRouterEnvelope(t, listRec, &listResp)
	if len(listResp.Items) != 0 {
		t.Fatalf("expected empty items, got %+v", listResp.Items)
	}

	threadsReq := httptest.NewRequest(http.MethodGet, "/api/threads", nil)
	threadsReq.Header.Set("Authorization", jwtAuth)
	threadsRec := httptest.NewRecorder()
	env.router.ServeHTTP(threadsRec, threadsReq)
	if threadsRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", threadsRec.Code, threadsRec.Body.String())
	}
	var threadsResp struct {
		Items        []service.T3ThreadSummary       `json:"items"`
		Environments []service.T3EnvironmentResponse `json:"environments"`
	}
	decodeRouterEnvelope(t, threadsRec, &threadsResp)
	if threadsResp.Items == nil || threadsResp.Environments == nil {
		t.Fatalf("expected non-nil arrays, got %+v", threadsResp)
	}

	// 不存在的环境 → 40417
	detailReq := httptest.NewRequest(http.MethodGet, "/api/environments/no-env/threads/th-1", nil)
	detailReq.Header.Set("Authorization", jwtAuth)
	detailRec := httptest.NewRecorder()
	env.router.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", detailRec.Code, detailRec.Body.String())
	}
	var envelope routerEnvelope
	if err := json.Unmarshal(detailRec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Code != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrT3EnvironmentNotFound.Code, envelope.Code)
	}

	// 条目端点同路径段路由可达：不存在的环境 → 40417
	itemReq := httptest.NewRequest(http.MethodGet, "/api/environments/no-env/threads/th-1/items/src::i1", nil)
	itemReq.Header.Set("Authorization", jwtAuth)
	itemRec := httptest.NewRecorder()
	env.router.ServeHTTP(itemRec, itemReq)
	if itemRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", itemRec.Code, itemRec.Body.String())
	}
	var itemEnvelope routerEnvelope
	if err := json.Unmarshal(itemRec.Body.Bytes(), &itemEnvelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if itemEnvelope.Code != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrT3EnvironmentNotFound.Code, itemEnvelope.Code)
	}

	// INT-85 写端点对 JWT 同样按路由可达（环境不存在 → 40417，先于 40300）
	writeCases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"composer options", http.MethodGet, "/api/environments/no-env/composer-options", nil},
		{"workspace options", http.MethodGet, "/api/environments/no-env/projects/p-1/workspace-options", nil},
		{"create thread", http.MethodPost, "/api/environments/no-env/threads", []byte(`{"project_id":"p-1","message":{"text":"hi"}}`)},
		{"send message", http.MethodPost, "/api/environments/no-env/threads/th-1/messages", []byte(`{"text":"hi"}`)},
		{"stop thread", http.MethodPost, "/api/environments/no-env/threads/th-1/stop", nil},
		{"edit queued", http.MethodPut, "/api/environments/no-env/threads/th-1/queue/run-1", []byte(`{"text":"x"}`)},
		{"respond request", http.MethodPost, "/api/environments/no-env/threads/th-1/requests/rr-1/respond", []byte(`{"type":"approval","decision":"accept"}`)},
		{"answer secret", http.MethodPost, "/api/environments/no-env/threads/th-1/items/th-1::s/secret-answer", []byte(`{"type":"decline"}`)},
		{"attachment content", http.MethodGet, "/api/environments/no-env/attachments/att-1/content", nil},
	}
	for _, tc := range writeCases {
		t.Run("jwt/"+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			if tc.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", jwtAuth)
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404 (env not found), got %d: %s", rec.Code, rec.Body.String())
			}
			var envelope routerEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.Code != errs.ErrT3EnvironmentNotFound.Code {
				t.Fatalf("expected code %d, got %d", errs.ErrT3EnvironmentNotFound.Code, envelope.Code)
			}
		})
	}
}
