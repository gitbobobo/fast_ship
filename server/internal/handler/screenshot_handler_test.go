package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
)

func newScreenshotUploadContext(t *testing.T, rec *httptest.ResponseRecorder, projectID, fileName string, content []byte, fields map[string]string) *gin.Context {
	t.Helper()
	req, _ := newMultipartUploadRequest(t, "/api/projects/"+projectID+"/screenshots", "file", fileName, content, fields)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = ginParams("id", projectID)
	return ctx
}

func setScreenshotJWTContext(ctx *gin.Context, userID, username string) {
	ctx.Set(middleware.ContextKeyUserID, userID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	ctx.Set(middleware.ContextKeyUserName, username)
}

func TestScreenshotHandlerUpload_ReturnsScreenAndVersion(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	rec := httptest.NewRecorder()
	ctx := newScreenshotUploadContext(t, rec, project.ID, "login.png", handlerTestPNGBytes, map[string]string{
		"screen_key": " Login ",
		"group":      "auth",
		"title":      "登录页",
		"note":       "首版",
	})
	setScreenshotJWTContext(ctx, user.ID, user.Username)

	env.screenshotHandler.Upload(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Screen struct {
			ID           string `json:"id"`
			ScreenKey    string `json:"screen_key"`
			Group        string `json:"group"`
			Title        string `json:"title"`
			VersionCount int    `json:"version_count"`
		} `json:"screen"`
		Version struct {
			ID         string `json:"id"`
			MimeType   string `json:"mime_type"`
			UploadedBy string `json:"uploaded_by"`
			ContentURL string `json:"content_url"`
		} `json:"version"`
	}
	decodeEnvelope(t, rec, &result)

	if result.Screen.ScreenKey != "login" || result.Screen.Group != "auth" || result.Screen.Title != "登录页" {
		t.Fatalf("unexpected screen payload: %+v", result.Screen)
	}
	if result.Screen.VersionCount != 1 {
		t.Fatalf("expected version_count 1, got %+v", result.Screen)
	}
	if result.Version.MimeType != "image/png" || result.Version.UploadedBy != user.Username {
		t.Fatalf("unexpected version payload: %+v", result.Version)
	}
	if !strings.HasSuffix(result.Version.ContentURL, "/api/screenshot-versions/"+result.Version.ID+"/content") {
		t.Fatalf("unexpected content_url %q", result.Version.ContentURL)
	}
}

func TestScreenshotHandlerUpload_APIKeyUploadedBy(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	rec := httptest.NewRecorder()
	ctx := newScreenshotUploadContext(t, rec, project.ID, "shot.png", handlerTestPNGBytes, map[string]string{
		"screen_key": "home",
	})
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	ctx.Set(middleware.ContextKeyAPIKey, "CI-Shot")

	env.screenshotHandler.Upload(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Version struct {
			UploadedBy string `json:"uploaded_by"`
		} `json:"version"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Version.UploadedBy != "API Key: CI-Shot" {
		t.Fatalf("expected uploaded_by from API key name, got %q", result.Version.UploadedBy)
	}
}

func TestScreenshotHandlerUpload_GroupPresenceSemantics(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	upload := func(fields map[string]string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx := newScreenshotUploadContext(t, rec, project.ID, "shot.png", handlerTestPNGBytes, fields)
		setScreenshotJWTContext(ctx, user.ID, user.Username)
		env.screenshotHandler.Upload(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("upload failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec
	}

	var first struct {
		Screen struct {
			ID    string `json:"id"`
			Group string `json:"group"`
			Title string `json:"title"`
		} `json:"screen"`
	}
	decodeEnvelope(t, upload(map[string]string{"screen_key": "home", "group": "g1", "title": "首页"}), &first)

	// 不携带 group/title 字段 → 两者保持
	var second struct {
		Screen struct {
			Group string `json:"group"`
			Title string `json:"title"`
		} `json:"screen"`
	}
	decodeEnvelope(t, upload(map[string]string{"screen_key": "home"}), &second)
	if second.Screen.Group != "g1" || second.Screen.Title != "首页" {
		t.Fatalf("expected group/title preserved, got %+v", second.Screen)
	}

	// group 字段出现且为空 → 置为未分组
	var third struct {
		Screen struct {
			Group string `json:"group"`
		} `json:"screen"`
	}
	decodeEnvelope(t, upload(map[string]string{"screen_key": "home", "group": ""}), &third)
	if third.Screen.Group != "" {
		t.Fatalf("expected empty group to clear, got %q", third.Screen.Group)
	}
}

func TestScreenshotHandlerUpload_RejectsMissingFileAndBadKey(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	rec := httptest.NewRecorder()
	ctx := newScreenshotUploadContext(t, rec, project.ID, "shot.png", handlerTestPNGBytes, nil)
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.screenshotHandler.Upload(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing screen_key, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScreenshotHandlerUpdate_BindsPointerFields(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	uploadRec := httptest.NewRecorder()
	uploadCtx := newScreenshotUploadContext(t, uploadRec, project.ID, "shot.png", handlerTestPNGBytes, map[string]string{
		"screen_key": "home",
		"group":      "g1",
		"title":      "首页",
	})
	setScreenshotJWTContext(uploadCtx, user.ID, user.Username)
	env.screenshotHandler.Upload(uploadCtx)

	var uploaded struct {
		Screen struct {
			ID string `json:"id"`
		} `json:"screen"`
	}
	decodeEnvelope(t, uploadRec, &uploaded)

	patchCtx, patchRec := newJSONContext(http.MethodPatch, "/api/screenshot-screens/"+uploaded.Screen.ID, []byte(`{"group":""}`))
	patchCtx.Params = ginParams("sid", uploaded.Screen.ID)
	setScreenshotJWTContext(patchCtx, user.ID, user.Username)
	env.screenshotHandler.Update(patchCtx)

	if patchRec.Code != http.StatusOK {
		t.Fatalf("expected patch 200, got %d: %s", patchRec.Code, patchRec.Body.String())
	}
	var updated struct {
		Group    string `json:"group"`
		Title    string `json:"title"`
		Versions []struct {
			ID string `json:"id"`
		} `json:"versions"`
	}
	decodeEnvelope(t, patchRec, &updated)
	if updated.Group != "" || updated.Title != "首页" {
		t.Fatalf("expected group cleared only, got %+v", updated)
	}
	if len(updated.Versions) != 1 {
		t.Fatalf("expected detail shape with versions, got %+v", updated)
	}
}

func TestScreenshotHandlerContent_ServesStoredImage(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	uploadRec := httptest.NewRecorder()
	uploadCtx := newScreenshotUploadContext(t, uploadRec, project.ID, "shot.png", handlerTestPNGBytes, map[string]string{
		"screen_key": "home",
	})
	setScreenshotJWTContext(uploadCtx, user.ID, user.Username)
	env.screenshotHandler.Upload(uploadCtx)

	var uploaded struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	decodeEnvelope(t, uploadRec, &uploaded)

	contentRec := httptest.NewRecorder()
	contentCtx, _ := gin.CreateTestContext(contentRec)
	contentCtx.Request = httptest.NewRequest(http.MethodGet, "/api/screenshot-versions/"+uploaded.Version.ID+"/content", nil)
	contentCtx.Params = ginParams("vid", uploaded.Version.ID)
	contentCtx.Set(middleware.ContextKeyUserID, user.ID)
	env.screenshotHandler.Content(contentCtx)

	if contentRec.Code != http.StatusOK {
		t.Fatalf("expected content 200, got %d: %s", contentRec.Code, contentRec.Body.String())
	}
	if ct := contentRec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected image/png content type, got %q", ct)
	}
	if cd := contentRec.Header().Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("expected inline disposition, got %q", cd)
	}
	if contentRec.Body.String() != string(handlerTestPNGBytes) {
		t.Fatalf("unexpected content body (%d bytes)", contentRec.Body.Len())
	}
}
