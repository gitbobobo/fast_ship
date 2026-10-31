package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/config"
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

func uploadHandlerScreen(t *testing.T, env *handlerTestEnv, projectID, userID, username string, fields map[string]string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx := newScreenshotUploadContext(t, rec, projectID, "shot.png", handlerTestPNGBytes, fields)
	setScreenshotJWTContext(ctx, userID, username)
	env.screenshotHandler.Upload(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload failed: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Screen struct {
			ID string `json:"id"`
		} `json:"screen"`
	}
	decodeEnvelope(t, rec, &result)
	return result.Screen.ID
}

func TestScreenshotHandlerUpload_ReturnsScreen(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	rec := httptest.NewRecorder()
	ctx := newScreenshotUploadContext(t, rec, project.ID, "login.png", handlerTestPNGBytes, map[string]string{
		"screen_key": " Login ",
		"platform":   "Desktop",
		"title":      "登录页",
	})
	setScreenshotJWTContext(ctx, user.ID, user.Username)

	env.screenshotHandler.Upload(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		Screen struct {
			ID         string  `json:"id"`
			ScreenKey  string  `json:"screen_key"`
			Platform   *string `json:"platform"`
			Title      string  `json:"title"`
			MimeType   string  `json:"mime_type"`
			UploadedBy string  `json:"uploaded_by"`
			ContentURL string  `json:"content_url"`
		} `json:"screen"`
	}
	decodeEnvelope(t, rec, &result)

	if result.Screen.ScreenKey != "login" || result.Screen.Title != "登录页" {
		t.Fatalf("unexpected screen payload: %+v", result.Screen)
	}
	if result.Screen.Platform == nil || *result.Screen.Platform != "desktop" {
		t.Fatalf("expected normalized platform, got %+v", result.Screen.Platform)
	}
	if result.Screen.MimeType != "image/png" || result.Screen.UploadedBy != user.Username {
		t.Fatalf("unexpected file info: %+v", result.Screen)
	}
	// content_url 带随文件变化的 ?v= 修订参数（覆盖上传即换 URL，缓存与
	// 画布指纹跟着失效）
	if !strings.HasPrefix(result.Screen.ContentURL, "/api/screenshot-screens/"+result.Screen.ID+"/content?v=") {
		t.Fatalf("unexpected content_url %q", result.Screen.ContentURL)
	}
	if v := result.Screen.ContentURL[strings.LastIndex(result.Screen.ContentURL, "=")+1:]; v == "" {
		t.Fatalf("expected non-empty revision param, got %q", result.Screen.ContentURL)
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
		Screen struct {
			UploadedBy string `json:"uploaded_by"`
		} `json:"screen"`
	}
	decodeEnvelope(t, rec, &result)
	if result.Screen.UploadedBy != "API Key: CI-Shot" {
		t.Fatalf("expected uploaded_by from API key name, got %q", result.Screen.UploadedBy)
	}
}

func TestScreenshotHandlerUpload_DimensionPresenceSemantics(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	upload := func(fields map[string]string) string {
		return uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, fields)
	}

	screenID := upload(map[string]string{
		"screen_key": "home", "platform": "ios", "locale": "en-US", "theme": "light", "title": "首页",
	})

	// 带同样维度再传：覆盖同一条截图
	againID := upload(map[string]string{
		"screen_key": "home", "platform": "ios", "locale": "en-us", "theme": "light",
	})
	if againID != screenID {
		t.Fatalf("expected normalized dims to hit same screen, got %q vs %q", againID, screenID)
	}

	// 不携带维度字段 → 文档语义「不出现保持原值」：唯一同 key 变体被继承并覆盖
	inheritID := upload(map[string]string{"screen_key": "home"})
	if inheritID != screenID {
		t.Fatalf("expected omitted dims to inherit and overwrite the sole variant, got %q vs %q", inheritID, screenID)
	}

	// theme 非法 → 40001
	rec := httptest.NewRecorder()
	ctx := newScreenshotUploadContext(t, rec, project.ID, "shot.png", handlerTestPNGBytes, map[string]string{
		"screen_key": "home", "theme": "blue",
	})
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.screenshotHandler.Upload(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad theme, got %d: %s", rec.Code, rec.Body.String())
	}

	// locale 非法 → 40001
	rec = httptest.NewRecorder()
	ctx = newScreenshotUploadContext(t, rec, project.ID, "shot.png", handlerTestPNGBytes, map[string]string{
		"screen_key": "home", "locale": "not ok!",
	})
	setScreenshotJWTContext(ctx, user.ID, user.Username)
	env.screenshotHandler.Upload(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad locale, got %d: %s", rec.Code, rec.Body.String())
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

	screenID := uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "home",
		"title":      "首页",
	})

	patchCtx, patchRec := newJSONContext(http.MethodPatch, "/api/screenshot-screens/"+screenID, []byte(`{"platform":"","locale":"zh-Hans-CN","theme":"dark"}`))
	patchCtx.Params = ginParams("sid", screenID)
	setScreenshotJWTContext(patchCtx, user.ID, user.Username)
	env.screenshotHandler.Update(patchCtx)

	if patchRec.Code != http.StatusOK {
		t.Fatalf("expected patch 200, got %d: %s", patchRec.Code, patchRec.Body.String())
	}
	var updated struct {
		Platform *string `json:"platform"`
		Locale   *string `json:"locale"`
		Theme    *string `json:"theme"`
		Title    string  `json:"title"`
	}
	decodeEnvelope(t, patchRec, &updated)
	if updated.Platform != nil || updated.Title != "首页" {
		t.Fatalf("expected platform cleared only, got %+v", updated)
	}
	if updated.Locale == nil || *updated.Locale != "zh-Hans-CN" {
		t.Fatalf("expected normalized locale, got %+v", updated.Locale)
	}
	if updated.Theme == nil || *updated.Theme != "dark" {
		t.Fatalf("expected theme dark, got %+v", updated.Theme)
	}

	// 撞变体唯一索引 → 409：把 fr 变体的 locale 改成已被 en 变体占用的组合
	xID := uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "conflict", "platform": "ios", "locale": "en-US", "theme": "light",
	})
	yID := uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "conflict", "platform": "ios", "locale": "fr-FR", "theme": "light",
	})
	conflictCtx, conflictRec := newJSONContext(http.MethodPatch, "/api/screenshot-screens/"+yID, []byte(`{"locale":"en-US"}`))
	conflictCtx.Params = ginParams("sid", yID)
	setScreenshotJWTContext(conflictCtx, user.ID, user.Username)
	env.screenshotHandler.Update(conflictCtx)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for variant conflict, got %d: %s", conflictRec.Code, conflictRec.Body.String())
	}

	// 同值更新（自己撞自己）不误报
	okCtx, okRec := newJSONContext(http.MethodPatch, "/api/screenshot-screens/"+yID, []byte(`{"locale":"fr-FR"}`))
	okCtx.Params = ginParams("sid", yID)
	setScreenshotJWTContext(okCtx, user.ID, user.Username)
	env.screenshotHandler.Update(okCtx)
	if okRec.Code != http.StatusOK {
		t.Fatalf("expected same-value patch accepted, got %d: %s", okRec.Code, okRec.Body.String())
	}
	_ = xID
}

func TestScreenshotHandlerContent_ServesStoredImage(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	screenID := uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "home",
	})

	contentRec := httptest.NewRecorder()
	contentCtx, _ := gin.CreateTestContext(contentRec)
	contentCtx.Request = httptest.NewRequest(http.MethodGet, "/api/screenshot-screens/"+screenID+"/content", nil)
	contentCtx.Params = ginParams("sid", screenID)
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
	// 同源内容出口不变量：截图是用户上传内容且认 ?token=，必须带硬化头。
	if got := contentRec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
	if got := contentRec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; sandbox" {
		t.Fatalf("expected sandbox CSP, got %q", got)
	}
	if contentRec.Body.String() != string(handlerTestPNGBytes) {
		t.Fatalf("unexpected content body (%d bytes)", contentRec.Body.Len())
	}
}

func TestScreenshotHandlerList_DimensionFilters(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "a",
	})
	uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "b", "platform": "ios",
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/screenshots?platform=__none__", nil)
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.screenshotHandler.List(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Items []struct {
			ScreenKey string  `json:"screen_key"`
			Platform  *string `json:"platform"`
		} `json:"items"`
	}
	decodeEnvelope(t, rec, &result)
	if len(result.Items) != 1 || result.Items[0].ScreenKey != "a" || result.Items[0].Platform != nil {
		t.Fatalf("expected only untagged screen, got %+v", result.Items)
	}
}

// M2 回归：GET 组合过滤按维度分治——locale 过滤值与库存里规范化后的
// en-US 对上（过滤传 en-us / EN-US 都能命中），theme 只认 light|dark，
// 非法 locale 返回 40001。
func TestScreenshotHandlerList_NormalizesDimensionFilters(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)

	uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "en", "platform": "Desktop", "locale": "en-US", "theme": "light",
	})
	uploadHandlerScreen(t, env, project.ID, user.ID, user.Username, map[string]string{
		"screen_key": "zh", "locale": "zh-Hans-CN", "theme": "dark",
	})

	list := func(query string) ([]struct {
		ScreenKey string `json:"screen_key"`
	}, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/screenshots?"+query, nil)
		ctx.Params = ginParams("id", project.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.screenshotHandler.List(ctx)
		var result struct {
			Items []struct {
				ScreenKey string `json:"screen_key"`
			} `json:"items"`
		}
		if rec.Code == http.StatusOK {
			decodeEnvelope(t, rec, &result)
		}
		return result.Items, rec.Code
	}

	// 组合：platform 大小写 + locale 大小写（en-us 命中 en-US）+ theme
	items, code := list("platform=desktop&locale=en-us&theme=light")
	if code != http.StatusOK || len(items) != 1 || items[0].ScreenKey != "en" {
		t.Fatalf("combined filter: %d items, code=%d", len(items), code)
	}
	// 未标注桶与命中值混用：库里没有 locale 为 NULL 的截图，只有 zh 命中
	items, code = list("locale=zh-Hans-CN,__none__")
	if code != http.StatusOK || len(items) != 1 || items[0].ScreenKey != "zh" {
		t.Fatalf("locale value + none bucket: %d items, code=%d", len(items), code)
	}
	// 传匹配不到的 locale → 空列表（而不是按原样匹配失败语义）
	items, code = list("locale=fr-FR")
	if code != http.StatusOK || len(items) != 0 {
		t.Fatalf("mismatched locale: %d items, code=%d", len(items), code)
	}
	// 非法 locale / theme → 40001
	if _, code = list("locale=en-a"); code != http.StatusBadRequest {
		t.Fatalf("illegal locale filter: expected 400, got %d", code)
	}
	if _, code = list("theme=blue"); code != http.StatusBadRequest {
		t.Fatalf("illegal theme filter: expected 400, got %d", code)
	}
}

func TestScreenshotHandlerUpload_RejectsOversizedBody(t *testing.T) {
	// 请求体超限时应在 multipart 解析阶段返回 413，而不是先落临时盘再被 service 拒
	h := NewScreenshotHandler(nil, &config.Config{
		Upload: config.UploadConfig{MaxFileSize: 1 << 10},
	})
	rec := httptest.NewRecorder()
	big := bytes.Repeat(handlerTestPNGBytes, 70000)
	ctx := newScreenshotUploadContext(t, rec, "proj-1", "big.png", big, map[string]string{
		"screen_key": "home",
	})
	setScreenshotJWTContext(ctx, "user-1", "user-1")

	h.Upload(ctx)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}
