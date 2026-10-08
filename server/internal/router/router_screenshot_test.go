package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
)

func createRouterScreenshotAPIKey(t *testing.T, env *routerTestEnv, userID, name, rawKey string) string {
	t.Helper()
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    userID,
		Name:      name,
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	return "Bearer " + service.FormatApiKey(rawKey)
}

func TestRouterScreenshotJWTFlow_UploadListDetailContent(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shotuser", "shotuser@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	uploadReq := newRouterMultipartRequest(t, "/api/projects/"+project.ID+"/screenshots", "file", "login.png", routerTestPNGBytes, map[string]string{
		"screen_key": " Login ",
		"group":      "auth",
		"title":      "登录页",
		"note":       "首版",
	})
	uploadReq.Header.Set("Authorization", "Bearer "+auth.Token)
	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var uploaded struct {
		Screen struct {
			ID        string `json:"id"`
			ScreenKey string `json:"screen_key"`
		} `json:"screen"`
		Version struct {
			ID         string `json:"id"`
			ContentURL string `json:"content_url"`
		} `json:"version"`
	}
	decodeRouterEnvelope(t, uploadRec, &uploaded)
	if uploaded.Screen.ScreenKey != "login" {
		t.Fatalf("expected normalized screen_key, got %+v", uploaded.Screen)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/screenshots", nil)
	listReq.Header.Set("Authorization", "Bearer "+auth.Token)
	listRec := httptest.NewRecorder()
	env.router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var list struct {
		Items []struct {
			ID            string `json:"id"`
			VersionCount  int    `json:"version_count"`
			LatestVersion *struct {
				ID string `json:"id"`
			} `json:"latest_version"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, listRec, &list)
	if len(list.Items) != 1 || list.Items[0].VersionCount != 1 {
		t.Fatalf("unexpected list payload: %+v", list.Items)
	}
	if list.Items[0].LatestVersion == nil || list.Items[0].LatestVersion.ID != uploaded.Version.ID {
		t.Fatalf("expected latest_version populated, got %+v", list.Items[0].LatestVersion)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/screenshot-screens/"+uploaded.Screen.ID, nil)
	detailReq.Header.Set("Authorization", "Bearer "+auth.Token)
	detailRec := httptest.NewRecorder()
	env.router.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("expected detail 200, got %d: %s", detailRec.Code, detailRec.Body.String())
	}
	var detail struct {
		Versions []struct {
			ID string `json:"id"`
		} `json:"versions"`
	}
	decodeRouterEnvelope(t, detailRec, &detail)
	if len(detail.Versions) != 1 || detail.Versions[0].ID != uploaded.Version.ID {
		t.Fatalf("unexpected versions in detail: %+v", detail.Versions)
	}

	contentReq := httptest.NewRequest(http.MethodGet, uploaded.Version.ContentURL+"?token="+auth.Token, nil)
	contentRec := httptest.NewRecorder()
	env.router.ServeHTTP(contentRec, contentReq)
	if contentRec.Code != http.StatusOK {
		t.Fatalf("expected content 200, got %d: %s", contentRec.Code, contentRec.Body.String())
	}
	if ct := contentRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected image/png, got %q", ct)
	}
	if !bytes.Equal(contentRec.Body.Bytes(), routerTestPNGBytes) {
		t.Fatalf("content bytes mismatch")
	}
}

func TestRouterScreenshotAPIKeyPermissions(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shotowner", "shotowner@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	apiKeyAuth := createRouterScreenshotAPIKey(t, env, auth.UserID, "CI-Shot", "SHOTAPIKEY1234567890")

	// API Key 可上传
	uploadReq := newRouterMultipartRequest(t, "/api/projects/"+project.ID+"/screenshots", "file", "shot.png", routerTestPNGBytes, map[string]string{
		"screen_key": "home",
	})
	uploadReq.Header.Set("Authorization", apiKeyAuth)
	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected API key upload 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploaded struct {
		Screen struct {
			ID string `json:"id"`
		} `json:"screen"`
		Version struct {
			ID         string `json:"id"`
			UploadedBy string `json:"uploaded_by"`
		} `json:"version"`
	}
	decodeRouterEnvelope(t, uploadRec, &uploaded)
	if uploaded.Version.UploadedBy != "API Key: CI-Shot" {
		t.Fatalf("expected uploaded_by API key name, got %q", uploaded.Version.UploadedBy)
	}

	// API Key 可读列表/详情/content
	for _, path := range []string{
		"/api/projects/" + project.ID + "/screenshots",
		"/api/screenshot-screens/" + uploaded.Screen.ID,
		"/api/screenshot-versions/" + uploaded.Version.ID + "/content",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", apiKeyAuth)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("API key GET %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	// API Key 调 PATCH / DELETE 一律 403（仅 JWT）
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/screenshot-screens/"+uploaded.Screen.ID, bytes.NewReader([]byte(`{"group":"x"}`)))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Authorization", apiKeyAuth)
	patchRec := httptest.NewRecorder()
	env.router.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusForbidden {
		t.Fatalf("expected API key PATCH 403, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	for _, path := range []string{
		"/api/screenshot-screens/" + uploaded.Screen.ID,
		"/api/screenshot-versions/" + uploaded.Version.ID,
	} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.Header.Set("Authorization", apiKeyAuth)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("API key DELETE %s expected 403, got %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestRouterScreenshotScreenNotFound(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shotnf", "shotnf@example.com", "Password123")

	missingID := uuid.NewString()

	screenReq := httptest.NewRequest(http.MethodGet, "/api/screenshot-screens/"+missingID, nil)
	screenReq.Header.Set("Authorization", "Bearer "+auth.Token)
	screenRec := httptest.NewRecorder()
	env.router.ServeHTTP(screenRec, screenReq)
	if screenRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing screen, got %d: %s", screenRec.Code, screenRec.Body.String())
	}
	var screenErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(screenRec.Body.Bytes(), &screenErr); err != nil {
		t.Fatalf("decode screen error: %v", err)
	}
	if screenErr.Code != 40413 {
		t.Fatalf("expected error code 40413, got %d", screenErr.Code)
	}

	versionReq := httptest.NewRequest(http.MethodGet, "/api/screenshot-versions/"+missingID+"/content", nil)
	versionReq.Header.Set("Authorization", "Bearer "+auth.Token)
	versionRec := httptest.NewRecorder()
	env.router.ServeHTTP(versionRec, versionReq)
	if versionRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing version content, got %d: %s", versionRec.Code, versionRec.Body.String())
	}
	var versionErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(versionRec.Body.Bytes(), &versionErr); err != nil {
		t.Fatalf("decode version error: %v", err)
	}
	if versionErr.Code != 40414 {
		t.Fatalf("expected error code 40414, got %d", versionErr.Code)
	}
}
