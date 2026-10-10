package router

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// crop 需要可解码的真实位图，routerTestPNGBytes 只有文件头
func makeRouterTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(20 + x*2), uint8(20 + y*2), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func routerServeJSON(t *testing.T, env *routerTestEnv, method, path, authorization string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	return rec
}

// 通过路由上传一张可解码的截图，返回 version_id。JWT 流程。
func uploadRouterScreenshot(t *testing.T, env *routerTestEnv, token, projectID string, content []byte) string {
	t.Helper()
	uploadReq := newRouterMultipartRequest(t, "/api/projects/"+projectID+"/screenshots", "file", "shot.png", content, map[string]string{
		"screen_key": "home",
	})
	uploadReq.Header.Set("Authorization", "Bearer "+token)
	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("upload screenshot: %d: %s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploaded struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	decodeRouterEnvelope(t, uploadRec, &uploaded)
	return uploaded.Version.ID
}

func TestRouterScreenshotAnnotation_AuthMatrix(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "annowner", "annowner@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	jwtAuth := "Bearer " + auth.Token
	apiKeyAuth := createRouterScreenshotAPIKey(t, env, auth.UserID, "CI-Ann", "ANNAPIKEY1234567890")

	versionID := uploadRouterScreenshot(t, env, auth.Token, project.ID, makeRouterTestPNG(t, 100, 50))
	createBody, _ := json.Marshal(map[string]any{
		"x": 0.1, "y": 0.2, "width": 0.4, "height": 0.4, "body": "路由创建",
	})

	// JWT 创建 → 200
	createRec := routerServeJSON(t, env, http.MethodPost, "/api/screenshot-versions/"+versionID+"/annotations", jwtAuth, createBody)
	if createRec.Code != http.StatusOK {
		t.Fatalf("jwt create: %d: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decodeRouterEnvelope(t, createRec, &created)
	annotationID := created.ID

	// API Key 创建/删除 → 40301；JWT 列表/更新/裁剪 → 200
	cases := []struct {
		name   string
		method string
		path   string
		auth   string
		body   []byte
		status int
		code   int
	}{
		{"apikey create", http.MethodPost, "/api/screenshot-versions/" + versionID + "/annotations", apiKeyAuth, createBody, http.StatusForbidden, 40301},
		{"apikey delete", http.MethodDelete, "/api/screenshot-annotations/" + annotationID, apiKeyAuth, nil, http.StatusForbidden, 40301},
		{"apikey update resolve", http.MethodPut, "/api/screenshot-annotations/" + annotationID, apiKeyAuth, []byte(`{"status":"resolved"}`), http.StatusOK, 0},
		{"apikey update reopen", http.MethodPut, "/api/screenshot-annotations/" + annotationID, apiKeyAuth, []byte(`{"status":"open"}`), http.StatusForbidden, 40301},
		{"apikey update body", http.MethodPut, "/api/screenshot-annotations/" + annotationID, apiKeyAuth, []byte(`{"body":"x"}`), http.StatusForbidden, 40301},
		{"jwt list", http.MethodGet, "/api/projects/" + project.ID + "/screenshot-annotations", jwtAuth, nil, http.StatusOK, 0},
		{"apikey list", http.MethodGet, "/api/projects/" + project.ID + "/screenshot-annotations", apiKeyAuth, nil, http.StatusOK, 0},
		{"jwt update", http.MethodPut, "/api/screenshot-annotations/" + annotationID, jwtAuth, []byte(`{"status":"resolved"}`), http.StatusOK, 0},
		{"unauth list", http.MethodGet, "/api/projects/" + project.ID + "/screenshot-annotations", "", nil, http.StatusUnauthorized, 40100},
	}
	for _, tc := range cases {
		rec := routerServeJSON(t, env, tc.method, tc.path, tc.auth, tc.body)
		if rec.Code != tc.status {
			t.Fatalf("%s: expected status %d, got %d: %s", tc.name, tc.status, rec.Code, rec.Body.String())
		}
		if tc.code != 0 {
			var env_ routerEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env_); err != nil {
				t.Fatalf("%s: decode envelope: %v", tc.name, err)
			}
			if env_.Code != tc.code {
				t.Fatalf("%s: expected code %d, got %s", tc.name, tc.code, rec.Body.String())
			}
		}
	}

	// 无凭证 → 401
	if rec := routerServeJSON(t, env, http.MethodPost, "/api/screenshot-versions/"+versionID+"/annotations", "", createBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth create: expected 401, got %d", rec.Code)
	}
}

func TestRouterScreenshotAnnotation_CropQueryTokenAndHEAD(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "cropuser", "cropuser@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	versionID := uploadRouterScreenshot(t, env, auth.Token, project.ID, makeRouterTestPNG(t, 100, 50))
	createBody, _ := json.Marshal(map[string]any{
		"x": 0.1, "y": 0.2, "width": 0.4, "height": 0.4, "body": "裁剪",
	})
	createRec := routerServeJSON(t, env, http.MethodPost, "/api/screenshot-versions/"+versionID+"/annotations", "Bearer "+auth.Token, createBody)
	var created struct {
		ID      string `json:"id"`
		CropURL string `json:"crop_url"`
	}
	decodeRouterEnvelope(t, createRec, &created)

	// ?token= 直连
	getReq := httptest.NewRequest(http.MethodGet, created.CropURL+"?token="+auth.Token, nil)
	getRec := httptest.NewRecorder()
	env.router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("crop via query token: %d: %s", getRec.Code, getRec.Body.String())
	}
	if ct := getRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected image/png, got %q", ct)
	}
	if _, err := png.Decode(bytes.NewReader(getRec.Body.Bytes())); err != nil {
		t.Fatalf("crop body not PNG: %v", err)
	}

	// Authorization 头也可
	headReq := httptest.NewRequest(http.MethodHead, created.CropURL, nil)
	headReq.Header.Set("Authorization", "Bearer "+auth.Token)
	headRec := httptest.NewRecorder()
	env.router.ServeHTTP(headRec, headReq)
	if headRec.Code != http.StatusOK {
		t.Fatalf("head crop: %d", headRec.Code)
	}

	// 无凭证 → 401
	noAuthReq := httptest.NewRequest(http.MethodGet, created.CropURL, nil)
	noAuthRec := httptest.NewRecorder()
	env.router.ServeHTTP(noAuthRec, noAuthReq)
	if noAuthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth crop: expected 401, got %d", noAuthRec.Code)
	}
}

func TestRouterIssueDetail_EmbedsScreenshotAnnotations(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "issueann", "issueann@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	issue := createRouterTestIssue(t, env.db, project.ID)

	versionID := uploadRouterScreenshot(t, env, auth.Token, project.ID, makeRouterTestPNG(t, 100, 50))
	createBody, _ := json.Marshal(map[string]any{
		"x": 0.1, "y": 0.2, "width": 0.4, "height": 0.4, "body": "缺陷", "issue_id": issue.ID,
	})
	createRec := routerServeJSON(t, env, http.MethodPost, "/api/screenshot-versions/"+versionID+"/annotations", "Bearer "+auth.Token, createBody)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create with issue: %d: %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID             string  `json:"id"`
		IssueID        *string `json:"issue_id"`
		IssueReference *string `json:"issue_reference"`
	}
	decodeRouterEnvelope(t, createRec, &created)
	if created.IssueID == nil || *created.IssueID != issue.ID || created.IssueReference == nil {
		t.Fatalf("expected linked issue in create response, got %+v", created)
	}

	getRec := routerServeJSON(t, env, http.MethodGet, "/api/issues/"+issue.ID, "Bearer "+auth.Token, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("issue detail: %d: %s", getRec.Code, getRec.Body.String())
	}
	var detail struct {
		ScreenshotAnnotations []struct {
			ID      string  `json:"id"`
			IssueID *string `json:"issue_id"`
		} `json:"screenshot_annotations"`
	}
	decodeRouterEnvelope(t, getRec, &detail)
	if len(detail.ScreenshotAnnotations) != 1 || detail.ScreenshotAnnotations[0].ID != created.ID {
		t.Fatalf("expected embedded annotation, got %+v", detail.ScreenshotAnnotations)
	}
}
