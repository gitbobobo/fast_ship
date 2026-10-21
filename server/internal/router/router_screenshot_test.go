package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRouterScreenshotJWTFlow_UploadListContent(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shotuser", "shotuser@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	uploadReq := newRouterMultipartRequest(t, "/api/projects/"+project.ID+"/screenshots", "file", "login.png", routerTestPNGBytes, map[string]string{
		"screen_key": " Login ",
		"platform":   "Desktop",
		"locale":     "en-US",
		"title":      "登录页",
	})
	uploadReq.Header.Set("Authorization", "Bearer "+auth.Token)
	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var uploaded struct {
		Screen struct {
			ID         string `json:"id"`
			ScreenKey  string `json:"screen_key"`
			ContentURL string `json:"content_url"`
		} `json:"screen"`
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
			ID         string  `json:"id"`
			Platform   *string `json:"platform"`
			ContentURL string  `json:"content_url"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, listRec, &list)
	if len(list.Items) != 1 || list.Items[0].ID != uploaded.Screen.ID {
		t.Fatalf("unexpected list payload: %+v", list.Items)
	}
	if list.Items[0].Platform == nil || *list.Items[0].Platform != "desktop" {
		t.Fatalf("expected platform in list item, got %+v", list.Items[0].Platform)
	}

	// 维度过滤：platform=__none__ 排除掉已标注的这张；locale 过滤与库存里的
	// en-US 按规范化大小写匹配（M2 口径）
	filteredReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/screenshots?platform=__none__", nil)
	filteredReq.Header.Set("Authorization", "Bearer "+auth.Token)
	filteredRec := httptest.NewRecorder()
	env.router.ServeHTTP(filteredRec, filteredReq)
	if filteredRec.Code != http.StatusOK {
		t.Fatalf("expected filtered list 200, got %d: %s", filteredRec.Code, filteredRec.Body.String())
	}
	var filtered struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, filteredRec, &filtered)
	if len(filtered.Items) != 0 {
		t.Fatalf("expected filtered list empty, got %+v", filtered.Items)
	}

	localeReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/screenshots?locale=en-us", nil)
	localeReq.Header.Set("Authorization", "Bearer "+auth.Token)
	localeRec := httptest.NewRecorder()
	env.router.ServeHTTP(localeRec, localeReq)
	if localeRec.Code != http.StatusOK {
		t.Fatalf("expected locale filter 200, got %d: %s", localeRec.Code, localeRec.Body.String())
	}
	var localeFiltered struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, localeRec, &localeFiltered)
	if len(localeFiltered.Items) != 1 || localeFiltered.Items[0].ID != uploaded.Screen.ID {
		t.Fatalf("expected normalized locale filter to hit en-US screen, got %+v", localeFiltered.Items)
	}

	// content_url 自带 ?v= 修订参数，追加 token 凭证用 &；缓存键随 URL 变化
	if !strings.Contains(uploaded.Screen.ContentURL, "?v=") {
		t.Fatalf("expected versioned content_url, got %q", uploaded.Screen.ContentURL)
	}
	contentReq := httptest.NewRequest(http.MethodGet, uploaded.Screen.ContentURL+"&token="+auth.Token, nil)
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
			ID         string `json:"id"`
			UploadedBy string `json:"uploaded_by"`
		} `json:"screen"`
	}
	decodeRouterEnvelope(t, uploadRec, &uploaded)
	if uploaded.Screen.UploadedBy != "API Key: CI-Shot" {
		t.Fatalf("expected uploaded_by API key name, got %q", uploaded.Screen.UploadedBy)
	}

	// API Key 可读列表/content
	for _, path := range []string{
		"/api/projects/" + project.ID + "/screenshots",
		"/api/screenshot-screens/" + uploaded.Screen.ID + "/content",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", apiKeyAuth)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("API key GET %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
	}

	// API Key 调 PATCH / 标注创建 / 批量一律 403（仅 JWT）；删截图已对 API Key 开放，见 TestRouterScreenshotAPIKeyDelete
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/screenshot-screens/"+uploaded.Screen.ID, bytes.NewReader([]byte(`{"title":"x"}`)))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("Authorization", apiKeyAuth)
	patchRec := httptest.NewRecorder()
	env.router.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusForbidden {
		t.Fatalf("expected API key PATCH 403, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/screenshot-screens/" + uploaded.Screen.ID + "/annotations", `{"x":0.1,"y":0.1,"width":0.2,"height":0.2,"body":"x"}`},
		{http.MethodPost, "/api/projects/" + project.ID + "/screenshot-annotations/batch", `{"action":"resolve"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader([]byte(tc.body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", apiKeyAuth)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("API key %s %s expected 403, got %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestRouterScreenshotScreenNotFound(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shotnf", "shotnf@example.com", "Password123")

	missingID := uuid.NewString()

	contentReq := httptest.NewRequest(http.MethodGet, "/api/screenshot-screens/"+missingID+"/content", nil)
	contentReq.Header.Set("Authorization", "Bearer "+auth.Token)
	contentRec := httptest.NewRecorder()
	env.router.ServeHTTP(contentRec, contentReq)
	if contentRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing screen content, got %d: %s", contentRec.Code, contentRec.Body.String())
	}
	var contentErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(contentRec.Body.Bytes(), &contentErr); err != nil {
		t.Fatalf("decode content error: %v", err)
	}
	if contentErr.Code != 40413 {
		t.Fatalf("expected error code 40413, got %d", contentErr.Code)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/screenshot-screens/"+missingID, nil)
	deleteReq.Header.Set("Authorization", "Bearer "+auth.Token)
	deleteRec := httptest.NewRecorder()
	env.router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing screen delete, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// INT-145：界面可能增减，过时截图允许凭 API Key 删除——与 JWT 同语义（级联标注），
// 跨用户仍按不存在处理（40413），归属校验沿用凭证解析出的用户。
func TestRouterScreenshotAPIKeyDelete(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shotdelowner", "shotdelowner@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	apiKeyAuth := createRouterScreenshotAPIKey(t, env, auth.UserID, "CI-Del", "DELOWNERKEY1234567890")

	otherAuth := registerAndLoginInvitedRouterUser(t, env.router, auth.Token, "shotdelother", "shotdelother@example.com", "Password123")
	otherKeyAuth := createRouterScreenshotAPIKey(t, env, otherAuth.UserID, "CI-Other-Del", "DELSTRANGER1234567890")

	screenID := uploadRouterScreenshot(t, env, auth.Token, project.ID, makeRouterTestPNG(t, 100, 50))

	// 删除前先建一条标注（JWT），API Key 删截图后应随之级联消失
	annBody := []byte(`{"x":0.1,"y":0.1,"width":0.2,"height":0.2,"body":"删除级联"}`)
	createRec := routerServeJSON(t, env, http.MethodPost, "/api/screenshot-screens/"+screenID+"/annotations", "Bearer "+auth.Token, annBody)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create annotation: %d: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeRouterEnvelope(t, createRec, &created)
	if created.ID == "" {
		t.Fatalf("expected annotation id, got %s", createRec.Body.String())
	}

	// 其他用户的 API Key 删他人截图 → 404（40413），不 200/500，也不误删
	foreignRec := routerServeJSON(t, env, http.MethodDelete, "/api/screenshot-screens/"+screenID, otherKeyAuth, nil)
	if foreignRec.Code != http.StatusNotFound {
		t.Fatalf("foreign api key delete: expected 404, got %d: %s", foreignRec.Code, foreignRec.Body.String())
	}
	var foreignEnv routerEnvelope
	if err := json.Unmarshal(foreignRec.Body.Bytes(), &foreignEnv); err != nil {
		t.Fatalf("decode foreign delete error: %v", err)
	}
	if foreignEnv.Code != 40413 {
		t.Fatalf("expected foreign delete code 40413, got %d", foreignEnv.Code)
	}

	survivorRec := routerServeJSON(t, env, http.MethodGet, "/api/projects/"+project.ID+"/screenshots", "Bearer "+auth.Token, nil)
	if survivorRec.Code != http.StatusOK {
		t.Fatalf("list after foreign delete: %d: %s", survivorRec.Code, survivorRec.Body.String())
	}
	var survivorList struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, survivorRec, &survivorList)
	if len(survivorList.Items) != 1 || survivorList.Items[0].ID != screenID {
		t.Fatalf("expected screen to survive foreign delete, got %+v", survivorList.Items)
	}

	// 本人 API Key 删除自己的截图 → 200
	delRec := routerServeJSON(t, env, http.MethodDelete, "/api/screenshot-screens/"+screenID, apiKeyAuth, nil)
	if delRec.Code != http.StatusOK {
		t.Fatalf("api key delete: expected 200, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// content → 404（40413）
	contentRec := routerServeJSON(t, env, http.MethodGet, "/api/screenshot-screens/"+screenID+"/content", apiKeyAuth, nil)
	if contentRec.Code != http.StatusNotFound {
		t.Fatalf("content after delete: expected 404, got %d: %s", contentRec.Code, contentRec.Body.String())
	}
	var contentEnv routerEnvelope
	if err := json.Unmarshal(contentRec.Body.Bytes(), &contentEnv); err != nil {
		t.Fatalf("decode content error: %v", err)
	}
	if contentEnv.Code != 40413 {
		t.Fatalf("expected content code 40413, got %d", contentEnv.Code)
	}

	// 项目截图列表里消失
	listRec := routerServeJSON(t, env, http.MethodGet, "/api/projects/"+project.ID+"/screenshots", apiKeyAuth, nil)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list after delete: %d: %s", listRec.Code, listRec.Body.String())
	}
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, listRec, &list)
	if len(list.Items) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", list.Items)
	}

	// 级联断言：标注列表不再包含被删截图上的标注
	annRec := routerServeJSON(t, env, http.MethodGet, "/api/projects/"+project.ID+"/screenshot-annotations", apiKeyAuth, nil)
	if annRec.Code != http.StatusOK {
		t.Fatalf("annotation list after delete: %d: %s", annRec.Code, annRec.Body.String())
	}
	var annList struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, annRec, &annList)
	if len(annList.Items) != 0 {
		t.Fatalf("expected annotations cascaded away, got %+v", annList.Items)
	}

	// JWT 删除同样要活着：前端删除按钮走的就是这条路径，双凭证契约两个方向都钉住
	jwtScreenID := uploadRouterScreenshot(t, env, auth.Token, project.ID, makeRouterTestPNG(t, 100, 50))
	jwtDelRec := routerServeJSON(t, env, http.MethodDelete, "/api/screenshot-screens/"+jwtScreenID, "Bearer "+auth.Token, nil)
	if jwtDelRec.Code != http.StatusOK {
		t.Fatalf("jwt delete: expected 200, got %d: %s", jwtDelRec.Code, jwtDelRec.Body.String())
	}
	afterJWTDelRec := routerServeJSON(t, env, http.MethodGet, "/api/projects/"+project.ID+"/screenshots", "Bearer "+auth.Token, nil)
	if afterJWTDelRec.Code != http.StatusOK {
		t.Fatalf("list after jwt delete: %d: %s", afterJWTDelRec.Code, afterJWTDelRec.Body.String())
	}
	var afterJWTDelList struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, afterJWTDelRec, &afterJWTDelList)
	if len(afterJWTDelList.Items) != 0 {
		t.Fatalf("expected empty list after jwt delete, got %+v", afterJWTDelList.Items)
	}
}
