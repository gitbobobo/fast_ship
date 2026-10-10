package handler

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
)

// crop 需要可解码的真实位图，handlerTestPNGBytes 只有文件头
func makeHandlerTestPNG(t *testing.T, w, h int) []byte {
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

func uploadHandlerScreenshot(t *testing.T, env *handlerTestEnv, userID, username, projectID, screenKey string, content []byte) (screenID, versionID string) {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx := newScreenshotUploadContext(t, rec, projectID, "shot.png", content, map[string]string{
		"screen_key": screenKey,
	})
	setScreenshotJWTContext(ctx, userID, username)
	env.screenshotHandler.Upload(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload screenshot: %d: %s", rec.Code, rec.Body.String())
	}
	var uploaded struct {
		Screen struct {
			ID string `json:"id"`
		} `json:"screen"`
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	decodeEnvelope(t, rec, &uploaded)
	return uploaded.Screen.ID, uploaded.Version.ID
}

func createHandlerAnnotation(t *testing.T, env *handlerTestEnv, userID, username, versionID, body string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"x": 0.1, "y": 0.2, "width": 0.4, "height": 0.4, "body": body,
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/screenshot-versions/"+versionID+"/annotations", payload)
	ctx.Params = ginParams("vid", versionID)
	setScreenshotJWTContext(ctx, userID, username)
	env.annotationHandler.Create(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("create annotation: %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	decodeEnvelope(t, rec, &result)
	return result.ID
}

func setAnnotationAPIKeyContext(ctx *gin.Context, userID, keyName string) {
	ctx.Set(middleware.ContextKeyUserID, userID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	ctx.Set(middleware.ContextKeyAPIKey, keyName)
}

func TestScreenshotAnnotationHandlerCreate_JWTFlow(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	_, versionID := uploadHandlerScreenshot(t, env, user.ID, user.Username, project.ID, "home", makeHandlerTestPNG(t, 100, 50))

	payload, _ := json.Marshal(map[string]any{
		"x": 0.1, "y": 0.2, "width": 0.4, "height": 0.4, "body": "文字溢出",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/screenshot-versions/"+versionID+"/annotations", payload)
	ctx.Params = ginParams("vid", versionID)
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.annotationHandler.Create(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected create 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Body      string `json:"body"`
		CreatedBy string `json:"created_by"`
		ScreenKey string `json:"screen_key"`
		ImageURL  string `json:"image_url"`
		CropURL   string `json:"crop_url"`
		PixelRect struct {
			X, Y, Width, Height int
		} `json:"pixel_rect"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Status != "open" || result.Body != "文字溢出" || result.CreatedBy != user.Username {
		t.Fatalf("unexpected payload: %+v", result)
	}
	if result.ScreenKey != "home" || result.CropURL != "/api/screenshot-annotations/"+result.ID+"/crop" {
		t.Fatalf("unexpected urls: %+v", result)
	}
	if result.PixelRect.X != 10 || result.PixelRect.Y != 10 || result.PixelRect.Width != 40 || result.PixelRect.Height != 20 {
		t.Fatalf("unexpected pixel_rect: %+v", result.PixelRect)
	}
}

func TestScreenshotAnnotationHandlerCreate_RejectsInvalidRect(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	_, versionID := uploadHandlerScreenshot(t, env, user.ID, user.Username, project.ID, "home", makeHandlerTestPNG(t, 100, 50))

	payload, _ := json.Marshal(map[string]any{
		"x": 0.9, "y": 0.2, "width": 0.4, "height": 0.4, "body": "出界",
	})
	ctx, rec := newJSONContext(http.MethodPost, "/api/screenshot-versions/"+versionID+"/annotations", payload)
	ctx.Params = ginParams("vid", versionID)
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.annotationHandler.Create(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if decodeEnvelope(t, rec, nil).Code != 40001 {
		t.Fatalf("expected error code 40001, got %s", rec.Body.String())
	}
}

func TestScreenshotAnnotationHandlerList_Filters(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)
	screenID, versionID := uploadHandlerScreenshot(t, env, user.ID, user.Username, project.ID, "home", makeHandlerTestPNG(t, 100, 50))
	_, otherVersionID := uploadHandlerScreenshot(t, env, user.ID, user.Username, project.ID, "login", makeHandlerTestPNG(t, 100, 50))

	linked := createHandlerAnnotation(t, env, user.ID, user.Username, versionID, "home 标注")
	createHandlerAnnotation(t, env, user.ID, user.Username, otherVersionID, "login 标注")

	// 关联 issue
	linkPayload, _ := json.Marshal(map[string]any{"issue_id": issue.ID})
	linkCtx, linkRec := newJSONContext(http.MethodPut, "/api/screenshot-annotations/"+linked, linkPayload)
	linkCtx.Params = ginParams("aid", linked)
	setScreenshotJWTContext(linkCtx, user.ID, user.Username)
	env.annotationHandler.Update(linkCtx)
	if linkRec.Code != http.StatusOK {
		t.Fatalf("link issue: %d: %s", linkRec.Code, linkRec.Body.String())
	}

	list := func(query string) (int, []struct {
		ID string `json:"id"`
	}) {
		ctx, rec := newJSONContext(http.MethodGet, "/api/projects/"+project.ID+"/screenshot-annotations?"+query, nil)
		ctx.Params = ginParams("id", project.ID)
		setScreenshotJWTContext(ctx, user.ID, user.Username)
		env.annotationHandler.List(ctx)
		var result struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		decodeEnvelope(t, rec, &result)
		return rec.Code, result.Items
	}

	code, items := list("")
	if code != http.StatusOK || len(items) != 2 {
		t.Fatalf("unfiltered list: %d %+v", code, items)
	}
	code, items = list("issue_id=" + issue.ID)
	if code != http.StatusOK || len(items) != 1 || items[0].ID != linked {
		t.Fatalf("issue_id filter: %d %+v", code, items)
	}
	code, items = list("screen_id=" + screenID)
	if code != http.StatusOK || len(items) != 1 || items[0].ID != linked {
		t.Fatalf("screen_id filter: %d %+v", code, items)
	}
	code, items = list("status=resolved")
	if code != http.StatusOK || len(items) != 0 {
		t.Fatalf("status filter: %d %+v", code, items)
	}
	code, _ = list("status=bogus")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad status, got %d", code)
	}
}

func TestScreenshotAnnotationHandlerUpdate_APIKeyRestriction(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	_, versionID := uploadHandlerScreenshot(t, env, user.ID, user.Username, project.ID, "home", makeHandlerTestPNG(t, 100, 50))
	annotationID := createHandlerAnnotation(t, env, user.ID, user.Username, versionID, "初稿")

	update := func(payload string, apiKey bool) *httptest.ResponseRecorder {
		ctx, rec := newJSONContext(http.MethodPut, "/api/screenshot-annotations/"+annotationID, []byte(payload))
		ctx.Params = ginParams("aid", annotationID)
		if apiKey {
			setAnnotationAPIKeyContext(ctx, user.ID, "CI-Bot")
		} else {
			setScreenshotJWTContext(ctx, user.ID, user.Username)
		}
		env.annotationHandler.Update(ctx)
		return rec
	}

	// API Key：只允许纯 {"status":"resolved"}
	for _, payload := range []string{
		`{"status":"open"}`,
		`{"body":"改文案"}`,
		`{"issue_id":"issue-1"}`,
		`{"status":"resolved","body":"x"}`,
	} {
		rec := update(payload, true)
		if rec.Code != http.StatusForbidden || decodeEnvelope(t, rec, nil).Code != 40301 {
			t.Fatalf("api key payload %s: expected 40301, got %d: %s", payload, rec.Code, rec.Body.String())
		}
	}
	rec := update(`{"status":"resolved"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("api key resolve: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resolved struct {
		Status     string  `json:"status"`
		ResolvedAt *string `json:"resolved_at"`
	}
	decodeEnvelope(t, rec, &resolved)
	if resolved.Status != "resolved" || resolved.ResolvedAt == nil {
		t.Fatalf("expected resolved with timestamp, got %+v", resolved)
	}

	// JWT 不受该限制
	if rec := update(`{"status":"open","body":"jwt 可改"}`, false); rec.Code != http.StatusOK {
		t.Fatalf("jwt update: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScreenshotAnnotationHandler_NotFoundAndForeign(t *testing.T) {
	env := setupHandlerTestEnv(t)
	owner := createHandlerTestUser(t, env.db, "owner")
	stranger := createHandlerTestUser(t, env.db, "stranger")
	project := createHandlerTestProject(t, env.db, owner.ID)
	_, versionID := uploadHandlerScreenshot(t, env, owner.ID, owner.Username, project.ID, "home", makeHandlerTestPNG(t, 100, 50))
	annotationID := createHandlerAnnotation(t, env, owner.ID, owner.Username, versionID, "私有")

	for _, tc := range []struct {
		name string
		aid  string
		user string
	}{
		{"missing update", "missing-aid", owner.ID},
		{"foreign update", annotationID, stranger.ID},
		{"missing delete", "missing-aid", owner.ID},
		{"foreign delete", annotationID, stranger.ID},
		{"missing crop", "missing-aid", owner.ID},
		{"foreign crop", annotationID, stranger.ID},
	} {
		var rec *httptest.ResponseRecorder
		payload, _ := json.Marshal(map[string]any{"body": "x"})
		switch {
		case tc.name == "missing update" || tc.name == "foreign update":
			ctx, r := newJSONContext(http.MethodPut, "/api/screenshot-annotations/"+tc.aid, payload)
			ctx.Params = ginParams("aid", tc.aid)
			setScreenshotJWTContext(ctx, tc.user, "u")
			env.annotationHandler.Update(ctx)
			rec = r
		case tc.name == "missing delete" || tc.name == "foreign delete":
			ctx, r := newJSONContext(http.MethodDelete, "/api/screenshot-annotations/"+tc.aid, nil)
			ctx.Params = ginParams("aid", tc.aid)
			setScreenshotJWTContext(ctx, tc.user, "u")
			env.annotationHandler.Delete(ctx)
			rec = r
		default:
			ctx, r := newJSONContext(http.MethodGet, "/api/screenshot-annotations/"+tc.aid+"/crop", nil)
			ctx.Params = ginParams("aid", tc.aid)
			setScreenshotJWTContext(ctx, tc.user, "u")
			env.annotationHandler.Crop(ctx)
			rec = r
		}
		if rec.Code != http.StatusNotFound || decodeEnvelope(t, rec, nil).Code != 40416 {
			t.Fatalf("%s: expected 40416, got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestScreenshotAnnotationHandlerDeleteAndCrop(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	_, versionID := uploadHandlerScreenshot(t, env, user.ID, user.Username, project.ID, "home", makeHandlerTestPNG(t, 100, 50))
	annotationID := createHandlerAnnotation(t, env, user.ID, user.Username, versionID, "裁剪我")

	// crop 输出 PNG 与内容协商头
	ctx, rec := newJSONContext(http.MethodGet, "/api/screenshot-annotations/"+annotationID+"/crop", nil)
	ctx.Params = ginParams("aid", annotationID)
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.annotationHandler.Crop(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("crop: %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected image/png, got %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("expected inline disposition, got %q", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=300" {
		t.Fatalf("expected private cache, got %q", cc)
	}
	if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("crop body is not PNG: %v", err)
	}

	// delete 返回空成功，重复删 40416
	ctx, rec = newJSONContext(http.MethodDelete, "/api/screenshot-annotations/"+annotationID, nil)
	ctx.Params = ginParams("aid", annotationID)
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.annotationHandler.Delete(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d: %s", rec.Code, rec.Body.String())
	}

	ctx, rec = newJSONContext(http.MethodGet, "/api/screenshot-annotations/"+annotationID+"/crop", nil)
	ctx.Params = ginParams("aid", annotationID)
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.annotationHandler.Crop(ctx)
	if rec.Code != http.StatusNotFound || decodeEnvelope(t, rec, nil).Code != 40416 {
		t.Fatalf("deleted crop: expected 40416, got %d: %s", rec.Code, rec.Body.String())
	}
}
