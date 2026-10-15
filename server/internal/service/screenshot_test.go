package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

var testJPEGBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}

func uploadTestScreenshot(t *testing.T, svc *testServices, input *ScreenshotUploadInput) *ScreenshotUploadResult {
	t.Helper()
	result, err := svc.screenshotService.Upload(input)
	if err != nil {
		t.Fatalf("upload screenshot: %v", err)
	}
	return result
}

func newScreenshotUploadInput(projectID, userID, screenKey string) *ScreenshotUploadInput {
	return &ScreenshotUploadInput{
		ProjectID:  projectID,
		UserID:     userID,
		ScreenKey:  screenKey,
		FileName:   "shot.png",
		UploadedBy: "Web 用户",
		Reader:     bytes.NewReader(testPNGBytes),
	}
}

func storedScreenRow(t *testing.T, svc *testServices, screenID string) *model.ScreenshotScreen {
	t.Helper()
	screen, err := svc.screenshotRepo.FindScreenByID(screenID)
	if err != nil {
		t.Fatalf("find screen %q: %v", screenID, err)
	}
	return screen
}

func TestScreenshotServiceUpload_CreatesScreenWithFileInfo(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	input := newScreenshotUploadInput(project.ID, user.ID, "  Login Page  ")
	input.Platform = strPtr("Desktop")
	input.Title = "登录页"

	result := uploadTestScreenshot(t, svc, input)

	if result.Screen.ScreenKey != "login page" {
		t.Fatalf("expected normalized screen_key, got %q", result.Screen.ScreenKey)
	}
	if result.Screen.Platform == nil || *result.Screen.Platform != "desktop" {
		t.Fatalf("expected normalized platform, got %+v", result.Screen.Platform)
	}
	if result.Screen.Title != "登录页" {
		t.Fatalf("unexpected screen payload: %+v", result.Screen)
	}
	if result.Screen.MimeType != "image/png" {
		t.Fatalf("expected sniffed mime image/png, got %q", result.Screen.MimeType)
	}
	if result.Screen.FileSize != int64(len(testPNGBytes)) {
		t.Fatalf("expected file size %d, got %d", len(testPNGBytes), result.Screen.FileSize)
	}
	wantURL := fmt.Sprintf("/api/screenshot-screens/%s/content?v=%d", result.Screen.Id, storedScreenRow(t, svc, result.Screen.Id).UploadedAt.UnixNano())
	if result.Screen.ContentUrl != wantURL {
		t.Fatalf("expected content_url %q, got %q", wantURL, result.Screen.ContentUrl)
	}

	screen := storedScreenRow(t, svc, result.Screen.Id)
	if screen.FilePath == "" {
		t.Fatalf("expected stored file path on screen row")
	}
	exists, err := svc.storage.Exists(screen.FilePath)
	if err != nil || !exists {
		t.Fatalf("expected stored file at %q, exists=%v err=%v", screen.FilePath, exists, err)
	}
}

func TestScreenshotServiceUpload_OverwritesSameVariant(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	first := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "Login"))
	firstPath := storedScreenRow(t, svc, first.Screen.Id).FilePath
	time.Sleep(2 * time.Millisecond)
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, " LOGIN "))

	if first.Screen.Id != second.Screen.Id {
		t.Fatalf("expected same variant to overwrite one screen, got %q vs %q", first.Screen.Id, second.Screen.Id)
	}
	screen := storedScreenRow(t, svc, first.Screen.Id)
	if screen.FilePath == firstPath {
		t.Fatalf("expected file path to change on overwrite")
	}
	if screen.FileSize != int64(len(testPNGBytes)) {
		t.Fatalf("unexpected file size after overwrite: %d", screen.FileSize)
	}
	// 覆盖必须推进 uploaded_at（列表排序键）
	if !screen.UploadedAt.After(screen.CreatedAt) {
		t.Fatalf("expected uploaded_at bumped on overwrite, got %v (created %v)", screen.UploadedAt, screen.CreatedAt)
	}
	if exists, _ := svc.storage.Exists(firstPath); exists {
		t.Fatalf("expected replaced file %q removed after overwrite", firstPath)
	}
	if exists, _ := svc.storage.Exists(screen.FilePath); !exists {
		t.Fatalf("expected new file at %q", screen.FilePath)
	}

	var screenCount int64
	if err := svc.db.Model(&model.ScreenshotScreen{}).Count(&screenCount).Error; err != nil {
		t.Fatalf("count screens: %v", err)
	}
	if screenCount != 1 {
		t.Fatalf("expected 1 screen after overwrite, got %d", screenCount)
	}
}

// 覆盖上传后 content_url 必须跟着文件变（?v= 随 uploaded_at 推进）：
// URL 不变时浏览器画旧图、画布以 content_url 为换图指纹与草稿作废
// 依据的逻辑全部失效。
func TestScreenshotServiceUpload_OverwriteChangesContentURL(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	first := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "home"))
	time.Sleep(2 * time.Millisecond)
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "home"))

	if first.Screen.ContentUrl == second.Screen.ContentUrl {
		t.Fatalf("expected content_url to change on overwrite, got %q both times", first.Screen.ContentUrl)
	}
	row := storedScreenRow(t, svc, second.Screen.Id)
	want := fmt.Sprintf("/api/screenshot-screens/%s/content?v=%d", row.ID, row.UploadedAt.UnixNano())
	if second.Screen.ContentUrl != want {
		t.Fatalf("expected content_url %q derived from uploaded_at, got %q", want, second.Screen.ContentUrl)
	}

	// 标注响应的 image_url 同口径：带着当前文件的修订参数
	annotation, err := svc.annotationService.Create(second.Screen.Id, user.ID, "Web 用户", validAnnotationRequest())
	if err != nil {
		t.Fatalf("create annotation: %v", err)
	}
	if annotation.ImageUrl != want {
		t.Fatalf("expected image_url %q, got %q", want, annotation.ImageUrl)
	}
}

func TestScreenshotServiceUpload_VariantDimensionsDistinct(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	plain := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "home"))
	desktop := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "home")
		i.Platform = strPtr("Desktop")
		return i
	}())
	zh := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "home")
		i.Locale = strPtr("ZH-cn")
		return i
	}())
	dark := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "home")
		i.Theme = strPtr("dark")
		return i
	}())

	ids := map[string]bool{plain.Screen.Id: true, desktop.Screen.Id: true, zh.Screen.Id: true, dark.Screen.Id: true}
	if len(ids) != 4 {
		t.Fatalf("expected 4 distinct variants, got %d", len(ids))
	}
	if zh.Screen.Locale == nil || *zh.Screen.Locale != "zh-CN" {
		t.Fatalf("expected BCP 47 normalized locale zh-CN, got %+v", zh.Screen.Locale)
	}
	if desktop.Screen.Platform == nil || *desktop.Screen.Platform != "desktop" {
		t.Fatalf("expected lowercased platform, got %+v", desktop.Screen.Platform)
	}

	// 覆盖走维度匹配：再传一次 desktop 变体归并到同一行
	again := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, " HOME ")
		i.Platform = strPtr(" desktop ")
		return i
	}())
	if again.Screen.Id != desktop.Screen.Id {
		t.Fatalf("expected desktop variant to merge, got %q vs %q", again.Screen.Id, desktop.Screen.Id)
	}
}

func TestScreenshotServiceUpload_DimensionFormSemantics(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	first := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "settings")
		i.Platform = strPtr("ios")
		i.Locale = strPtr("en-US")
		i.Theme = strPtr("light")
		i.Title = "设置"
		return i
	}())

	// 带 full 维度再传：命中同一变体；title 非空才覆盖
	second := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "settings")
		i.Platform = strPtr("ios")
		i.Locale = strPtr(" en-us ")
		i.Theme = strPtr("light")
		return i
	}())
	if second.Screen.Id != first.Screen.Id {
		t.Fatalf("expected normalized dims to hit same variant, got %q vs %q", second.Screen.Id, first.Screen.Id)
	}
	if second.Screen.Title != "设置" {
		t.Fatalf("expected title preserved on empty input, got %q", second.Screen.Title)
	}
	if second.Screen.Locale == nil || *second.Screen.Locale != "en-US" {
		t.Fatalf("expected normalized locale, got %+v", second.Screen.Locale)
	}

	// 不携带维度字段 = 按「未标注」变体寻址：与 ios 变体是两行
	untagged := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "settings"))
	if untagged.Screen.Id == first.Screen.Id {
		t.Fatalf("expected missing dims to address the untagged variant")
	}
	if untagged.Screen.Platform != nil || untagged.Screen.Locale != nil || untagged.Screen.Theme != nil {
		t.Fatalf("expected untagged variant, got %+v", untagged.Screen)
	}

	// 显式空串 = 清除为未标注，与「缺失」寻址到同一未标注变体并覆盖它
	cleared := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "settings")
		i.Platform = strPtr("")
		i.Locale = strPtr(" ")
		i.Theme = strPtr("")
		return i
	}())
	if cleared.Screen.Id != untagged.Screen.Id {
		t.Fatalf("expected explicit empty dims to overwrite the untagged variant, got %q vs %q", cleared.Screen.Id, untagged.Screen.Id)
	}

	_ = first
}

func TestScreenshotServiceUpload_RejectsInvalidDimensions(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	cases := []struct {
		name   string
		mutate func(*ScreenshotUploadInput)
	}{
		{"locale not bcp47", func(i *ScreenshotUploadInput) { i.Locale = strPtr("not a locale!") }},
		{"locale too long", func(i *ScreenshotUploadInput) { i.Locale = strPtr(strings.Repeat("a", 36)) }},
		{"theme invalid", func(i *ScreenshotUploadInput) { i.Theme = strPtr("blue") }},
		{"theme case strict", func(i *ScreenshotUploadInput) { i.Theme = strPtr("Light") }},
		{"platform too long", func(i *ScreenshotUploadInput) { i.Platform = strPtr(strings.Repeat("p", 101)) }},
		{"title too long", func(i *ScreenshotUploadInput) { i.Title = strings.Repeat("t", maxScreenshotTitleRunes+1) }},
	}
	for _, tc := range cases {
		input := newScreenshotUploadInput(project.ID, user.ID, "home")
		tc.mutate(input)
		if _, err := svc.screenshotService.Upload(input); err != errs.ErrInvalidParams {
			t.Fatalf("%s: expected ErrInvalidParams, got %v", tc.name, err)
		}
	}

	// 合法形态放行：locale 大小写随意（会规范化）、空 theme 清除
	input := newScreenshotUploadInput(project.ID, user.ID, "home")
	input.Locale = strPtr("en-us")
	if _, err := svc.screenshotService.Upload(input); err != nil {
		t.Fatalf("expected en-us accepted, got %v", err)
	}
}

func TestScreenshotServiceUpload_RejectsInvalidContent(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	input := newScreenshotUploadInput(project.ID, user.ID, "login")
	input.Reader = bytes.NewReader([]byte("definitely not an image"))
	if _, err := svc.screenshotService.Upload(input); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for non-image, got %v", err)
	}

	input = newScreenshotUploadInput(project.ID, user.ID, "login")
	input.Reader = bytes.NewReader(nil)
	if _, err := svc.screenshotService.Upload(input); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty body, got %v", err)
	}
}

func TestScreenshotServiceUpload_AllowedMimeTypes(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	cases := []struct {
		name    string
		content []byte
		mime    string
	}{
		{"png", testPNGBytes, "image/png"},
		{"jpeg", testJPEGBytes, "image/jpeg"},
		{"gif", append([]byte("GIF89a"), testPNGBytes...), "image/gif"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "image/webp"},
	}
	for _, tc := range cases {
		input := newScreenshotUploadInput(project.ID, user.ID, tc.name)
		input.Reader = bytes.NewReader(tc.content)
		result, err := svc.screenshotService.Upload(input)
		if err != nil {
			t.Fatalf("%s: expected upload success, got %v", tc.name, err)
		}
		if result.Screen.MimeType != tc.mime {
			t.Fatalf("%s: expected mime %q, got %q", tc.name, tc.mime, result.Screen.MimeType)
		}
	}
}

func TestScreenshotServiceUpload_ScreenKeyValidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	for _, key := range []string{"", "   ", strings.Repeat("a", 101)} {
		input := newScreenshotUploadInput(project.ID, user.ID, key)
		if _, err := svc.screenshotService.Upload(input); err != errs.ErrInvalidParams {
			t.Fatalf("screen_key %q: expected ErrInvalidParams, got %v", key, err)
		}
	}

	input := newScreenshotUploadInput(project.ID, user.ID, strings.Repeat("a", 100))
	if _, err := svc.screenshotService.Upload(input); err != nil {
		t.Fatalf("expected 100-char key accepted, got %v", err)
	}
}

func TestScreenshotServiceUpload_RejectsOversizedFile(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	svc.cfg.Upload.MaxFileSize = int64(len(testPNGBytes))

	input := newScreenshotUploadInput(project.ID, user.ID, "login")
	oversized := append(append([]byte{}, testPNGBytes...), 0x00)
	input.Reader = bytes.NewReader(oversized)
	if _, err := svc.screenshotService.Upload(input); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for oversized file, got %v", err)
	}

	var screenCount int64
	if err := svc.db.Model(&model.ScreenshotScreen{}).Count(&screenCount).Error; err != nil {
		t.Fatalf("count screens: %v", err)
	}
	if screenCount != 0 {
		t.Fatalf("expected no screen row after rejected upload, got %d", screenCount)
	}
}

func TestScreenshotServiceUpload_RequiresOwnedProject(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	other := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, other.ID)

	input := newScreenshotUploadInput(project.ID, user.ID, "login")
	if _, err := svc.screenshotService.Upload(input); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for foreign project, got %v", err)
	}
	if _, err := svc.screenshotService.List(project.ID, user.ID, repository.ScreenshotScreenFilters{}); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound on list, got %v", err)
	}
}

func TestScreenshotServiceList_OrdersByUploadedAtDesc(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "alpha"))
	time.Sleep(2 * time.Millisecond)
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "beta"))
	time.Sleep(2 * time.Millisecond)
	third := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "alpha")
		i.Platform = strPtr("web")
		return i
	}())

	list, err := svc.screenshotService.List(project.ID, user.ID, repository.ScreenshotScreenFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 screens, got %d", len(list.Items))
	}
	if list.Items[0].ScreenKey != "alpha" || list.Items[1].ScreenKey != "beta" || list.Items[2].ScreenKey != "alpha" {
		t.Fatalf("expected uploaded_at desc order, got %+v", list.Items)
	}
	// 最新的是带 web 平台的 alpha 变体，其次 beta，最早的是未标注 alpha
	if list.Items[0].Id != third.Screen.Id || list.Items[1].Id != second.Screen.Id {
		t.Fatalf("unexpected item identity: %+v", list.Items)
	}
	if list.Items[2].Platform != nil {
		t.Fatalf("expected plain alpha variant last, got %+v", list.Items[2])
	}
}

func TestScreenshotServiceList_DimensionFilters(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	none := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "a"))
	ios := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "b")
		i.Platform = strPtr("ios")
		return i
	}())
	android := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "c")
		i.Platform = strPtr("android")
		i.Theme = strPtr("dark")
		return i
	}())

	list := func(filters repository.ScreenshotScreenFilters) []ScreenshotScreenResponse {
		t.Helper()
		res, err := svc.screenshotService.List(project.ID, user.ID, filters)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return res.Items
	}

	if got := list(repository.ScreenshotScreenFilters{Platforms: []string{"ios"}}); len(got) != 1 || got[0].Id != ios.Screen.Id {
		t.Fatalf("platform filter: %+v", got)
	}
	if got := list(repository.ScreenshotScreenFilters{Platforms: []string{"ios", "android"}}); len(got) != 2 {
		t.Fatalf("multi platform filter: %+v", got)
	}
	if got := list(repository.ScreenshotScreenFilters{Platforms: nil, IncludeNoPlat: true}); len(got) != 1 || got[0].Id != none.Screen.Id {
		t.Fatalf("none bucket filter: %+v", got)
	}
	if got := list(repository.ScreenshotScreenFilters{Platforms: []string{"ios"}, IncludeNoPlat: true}); len(got) != 2 {
		t.Fatalf("values + none bucket filter: %+v", got)
	}
	if got := list(repository.ScreenshotScreenFilters{Themes: []string{"dark"}}); len(got) != 1 || got[0].Id != android.Screen.Id {
		t.Fatalf("theme filter: %+v", got)
	}
	if got := list(repository.ScreenshotScreenFilters{Platforms: []string{"ios"}, Themes: []string{"dark"}}); len(got) != 0 {
		t.Fatalf("combo filter should exclude, got %+v", got)
	}
}

func TestScreenshotServiceGet_ForeignScreenNotFound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	other := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, other.ID)

	result := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, other.ID, "login"))

	if _, err := svc.screenshotRepo.FindScreenByID(result.Screen.Id); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// 跨用户统一按不存在处理
	if _, _, _, err := svc.screenshotService.GetScreenContent(result.Screen.Id, user.ID); err != errs.ErrScreenshotScreenNotFound {
		t.Fatalf("expected ErrScreenshotScreenNotFound for foreign screen content, got %v", err)
	}
	if err := svc.screenshotService.DeleteScreen(result.Screen.Id, user.ID); err != errs.ErrScreenshotScreenNotFound {
		t.Fatalf("expected ErrScreenshotScreenNotFound on delete, got %v", err)
	}
}

func TestScreenshotServiceUpdate_PointerSemantics(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	input := newScreenshotUploadInput(project.ID, user.ID, "login")
	input.Platform = strPtr("ios")
	input.Title = "登录"
	uploaded := uploadTestScreenshot(t, svc, input)

	// 仅 platform（空串→清除为未标注），title 不变
	updated, err := svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Platform: strPtr(""),
	})
	if err != nil {
		t.Fatalf("update platform: %v", err)
	}
	if updated.Platform != nil || updated.Title != "登录" {
		t.Fatalf("expected platform cleared and title preserved, got %+v", updated)
	}

	// 仅 title
	updated, err = svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Title: strPtr("新标题"),
	})
	if err != nil {
		t.Fatalf("update title: %v", err)
	}
	if updated.Title != "新标题" || updated.Platform != nil {
		t.Fatalf("expected title updated only, got %+v", updated)
	}

	// 维度规范化：locale 大小写归一
	updated, err = svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Locale: strPtr("zh-hans-cn"),
	})
	if err != nil {
		t.Fatalf("update locale: %v", err)
	}
	if updated.Locale == nil || *updated.Locale != "zh-Hans-CN" {
		t.Fatalf("expected normalized locale, got %+v", updated.Locale)
	}

	// 都不传 → 40001
	if _, err := svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty patch, got %v", err)
	}

	// 非法 theme → 40001
	bad := api.UpdateScreenshotScreenRequestTheme("blue")
	if _, err := svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Theme: &bad,
	}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for bad theme, got %v", err)
	}
}

func TestScreenshotServiceUpdate_VariantConflict(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	a := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "home")
		i.Platform = strPtr("ios")
		return i
	}())
	b := uploadTestScreenshot(t, svc, func() *ScreenshotUploadInput {
		i := newScreenshotUploadInput(project.ID, user.ID, "home")
		i.Platform = strPtr("android")
		return i
	}())

	// 把 b 改成 a 的维度组合 → 撞变体唯一索引 409
	_, err := svc.screenshotService.Update(b.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Platform: strPtr("ios"),
	})
	if err != errs.ErrScreenshotVariantExists {
		t.Fatalf("expected ErrScreenshotVariantExists, got %v", err)
	}

	// 同值更新（自己撞自己）不应误报冲突
	if _, err := svc.screenshotService.Update(b.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Platform: strPtr("android"),
	}); err != nil {
		t.Fatalf("expected same-value update accepted, got %v", err)
	}
	_ = a
}

func TestScreenshotServiceDeleteScreen_RemovesRowAndFile(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	uploaded := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))
	path := storedScreenRow(t, svc, uploaded.Screen.Id).FilePath

	if err := svc.screenshotService.DeleteScreen(uploaded.Screen.Id, user.ID); err != nil {
		t.Fatalf("delete screen: %v", err)
	}

	if _, err := svc.screenshotRepo.FindScreenByID(uploaded.Screen.Id); err == nil {
		t.Fatalf("expected screen row deleted")
	}
	if exists, _ := svc.storage.Exists(path); exists {
		t.Fatalf("expected file %q removed", path)
	}
}

// 删除与覆盖上传交错的孤儿文件回归：权限校验读与删除事务之间插入一次
// 覆盖（文件指针换到新路径），删除必须清理事务内重新读到的新路径，
// 存储目录不遗留任何未被引用的文件。
func TestScreenshotServiceDeleteScreen_CleansPathCurrentAtTx(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	uploaded := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "home"))
	stalePath := storedScreenRow(t, svc, uploaded.Screen.Id).FilePath
	currentPath := buildScreenshotStoragePath(project.ID, uploaded.Screen.Id, "interleave", "image/png")

	// 注入点在删除事务开启前触发：模拟覆盖上传恰在「权限校验读之后、
	// 删除事务之前」提交——文件指针换到新路径、新文件落盘、旧文件按
	// 覆盖流程清理
	svc.screenshotService.deleteScreenBeforeTx = func() {
		if err := svc.screenshotRepo.UpdateScreenByMap(uploaded.Screen.Id, map[string]interface{}{"file_path": currentPath}); err != nil {
			t.Errorf("interleave update: %v", err)
		}
		if err := svc.storage.Save(currentPath, bytes.NewReader(testPNGBytes)); err != nil {
			t.Errorf("interleave save: %v", err)
		}
		if err := svc.storage.Delete(stalePath); err != nil {
			t.Errorf("interleave cleanup: %v", err)
		}
	}

	if err := svc.screenshotService.DeleteScreen(uploaded.Screen.Id, user.ID); err != nil {
		t.Fatalf("delete screen: %v", err)
	}

	// 删除清理的是事务内重新读取的当前路径；旧文件已由覆盖流程删掉，
	// 存储目录仅剩被引用文件——此刻为空（孤儿数=0）
	if exists, _ := svc.storage.Exists(currentPath); exists {
		t.Fatalf("expected file %q (current at tx) removed by delete", currentPath)
	}
	if exists, _ := svc.storage.Exists(stalePath); exists {
		t.Fatalf("expected stale file %q to stay removed", stalePath)
	}
}

func TestScreenshotServiceGetScreenContent_ReturnsStoredBytes(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	result := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))

	reader, mimeType, fileSize, err := svc.screenshotService.GetScreenContent(result.Screen.Id, user.ID)
	if err != nil {
		t.Fatalf("get content: %v", err)
	}
	defer reader.Close()

	if mimeType != "image/png" {
		t.Fatalf("expected image/png, got %q", mimeType)
	}
	if fileSize != int64(len(testPNGBytes)) {
		t.Fatalf("expected file size %d, got %d", len(testPNGBytes), fileSize)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if !bytes.Equal(got, testPNGBytes) {
		t.Fatalf("content bytes mismatch")
	}
}

// 并发首传同一新变体：失败方撞唯一索引后整体重试，应归并到同一条截图行。
// 测试库为多连接共享内存库，能否真正交错取决于连接池时序；竞态未触发时
// 本用例退化为顺序覆盖路径仍有效。
func TestScreenshotServiceUpload_ConcurrentFirstUploadMerges(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	const n = 6
	start := make(chan struct{})
	results := make(chan *ScreenshotUploadResult, n)
	errsCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			<-start
			result, err := svc.screenshotService.Upload(newScreenshotUploadInput(project.ID, user.ID, "Concurrent"))
			if err != nil {
				errsCh <- err
				return
			}
			results <- result
		}()
	}
	close(start)

	screenIDs := map[string]bool{}
	for i := 0; i < n; i++ {
		select {
		case err := <-errsCh:
			t.Fatalf("concurrent upload %d failed: %v", i, err)
		case result := <-results:
			screenIDs[result.Screen.Id] = true
		}
	}
	if len(screenIDs) != 1 {
		t.Fatalf("expected all uploads to land on one screen, got %d", len(screenIDs))
	}
	for id := range screenIDs {
		screen := storedScreenRow(t, svc, id)
		if exists, _ := svc.storage.Exists(screen.FilePath); !exists {
			t.Fatalf("expected current file to exist at %q", screen.FilePath)
		}
	}
}

func TestIsRetryableScreenshotTxError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unique violation", errors.New("UNIQUE constraint failed: screenshot_screens.project_id, screenshot_screens.screen_key (2067)"), true},
		{"busy", errors.New("database is locked (5)"), true},
		{"table locked", errors.New("database table is locked: database is deadlocked (6)"), true},
		{"gorm dup key", gorm.ErrDuplicatedKey, true},
		{"record not found", gorm.ErrRecordNotFound, false},
		{"app error", errs.ErrInvalidParams, false},
	}
	for _, tc := range cases {
		if got := isRetryableScreenshotTxError(tc.err); got != tc.want {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
		}
	}
}

func TestIsScreenshotVariantConflict(t *testing.T) {
	if !isScreenshotVariantConflict(errors.New("UNIQUE constraint failed: screenshot_screens.project_id, screenshot_screens.screen_key")) {
		t.Fatalf("expected screenshot_screens unique error to be variant conflict")
	}
	if !isScreenshotVariantConflict(gorm.ErrDuplicatedKey) {
		t.Fatalf("expected gorm duplicated key to be variant conflict")
	}
	if isScreenshotVariantConflict(errors.New("UNIQUE constraint failed: users.name")) {
		t.Fatalf("expected other-table unique error not to be variant conflict")
	}
	if isScreenshotVariantConflict(nil) {
		t.Fatalf("expected nil not to be conflict")
	}
}
