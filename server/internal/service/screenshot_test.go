package service

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
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

func TestScreenshotServiceUpload_CreatesScreenAndVersion(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	input := newScreenshotUploadInput(project.ID, user.ID, "  Login Page  ")
	input.Group = api.Ptr("auth")
	input.Title = "登录页"
	input.Note = "首版"

	result := uploadTestScreenshot(t, svc, input)

	if result.Screen.ScreenKey != "login page" {
		t.Fatalf("expected normalized screen_key, got %q", result.Screen.ScreenKey)
	}
	if result.Screen.Group != "auth" || result.Screen.Title != "登录页" {
		t.Fatalf("unexpected screen payload: %+v", result.Screen)
	}
	if result.Screen.VersionCount != 1 {
		t.Fatalf("expected version_count 1, got %d", result.Screen.VersionCount)
	}
	if result.Version.ScreenId != result.Screen.Id {
		t.Fatalf("expected version linked to screen, got %+v", result.Version)
	}
	if result.Version.MimeType != "image/png" {
		t.Fatalf("expected sniffed mime image/png, got %q", result.Version.MimeType)
	}
	if result.Version.FileSize != int64(len(testPNGBytes)) {
		t.Fatalf("expected file size %d, got %d", len(testPNGBytes), result.Version.FileSize)
	}
	if result.Version.Note != "首版" || result.Version.UploadedBy != "Web 用户" {
		t.Fatalf("unexpected version payload: %+v", result.Version)
	}
	wantURL := "/api/screenshot-versions/" + result.Version.Id + "/content"
	if result.Version.ContentUrl != wantURL {
		t.Fatalf("expected content_url %q, got %q", wantURL, result.Version.ContentUrl)
	}

	version, err := svc.screenshotRepo.FindVersionByID(result.Version.Id)
	if err != nil {
		t.Fatalf("find version: %v", err)
	}
	exists, err := svc.storage.Exists(version.FilePath)
	if err != nil || !exists {
		t.Fatalf("expected stored file at %q, exists=%v err=%v", version.FilePath, exists, err)
	}
}

func TestScreenshotServiceUpload_MergesIntoExistingScreen(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	first := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "Login"))
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, " LOGIN "))
	other := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "home"))

	if first.Screen.Id != second.Screen.Id {
		t.Fatalf("expected normalized keys to merge into same screen, got %q vs %q", first.Screen.Id, second.Screen.Id)
	}
	if other.Screen.Id == first.Screen.Id {
		t.Fatalf("expected distinct screen for different key")
	}
	if second.Screen.VersionCount != 2 {
		t.Fatalf("expected version_count 2, got %d", second.Screen.VersionCount)
	}
	if first.Version.Id == second.Version.Id {
		t.Fatalf("expected distinct versions on merge")
	}
}

func TestScreenshotServiceUpload_GroupAndTitleFormSemantics(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	input := newScreenshotUploadInput(project.ID, user.ID, "settings")
	input.Group = api.Ptr("base")
	input.Title = "设置"
	uploadTestScreenshot(t, svc, input)

	// 不携带 group/title：两者均保持不变
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "settings"))
	if second.Screen.Group != "base" || second.Screen.Title != "设置" {
		t.Fatalf("expected group/title preserved, got %+v", second.Screen)
	}

	// group 出现（空串）→ 置为未分组；title 空串不更新
	input = newScreenshotUploadInput(project.ID, user.ID, "settings")
	input.Group = api.Ptr("")
	input.Title = ""
	third := uploadTestScreenshot(t, svc, input)
	if third.Screen.Group != "" {
		t.Fatalf("expected empty group to clear, got %q", third.Screen.Group)
	}
	if third.Screen.Title != "设置" {
		t.Fatalf("expected title preserved on empty input, got %q", third.Screen.Title)
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
		if result.Version.MimeType != tc.mime {
			t.Fatalf("%s: expected mime %q, got %q", tc.name, tc.mime, result.Version.MimeType)
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
	if _, err := svc.screenshotService.List(project.ID, user.ID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound on list, got %v", err)
	}
}

func TestScreenshotServiceList_OrdersByLastUploadedAtDesc(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "alpha"))
	time.Sleep(2 * time.Millisecond)
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "beta"))
	time.Sleep(2 * time.Millisecond)
	third := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "alpha"))

	list, err := svc.screenshotService.List(project.ID, user.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 screens, got %d", len(list.Items))
	}
	if list.Items[0].ScreenKey != "alpha" || list.Items[1].ScreenKey != "beta" {
		t.Fatalf("expected last_uploaded_at desc order, got %+v", list.Items)
	}
	if list.Items[0].VersionCount != 2 || list.Items[1].VersionCount != 1 {
		t.Fatalf("unexpected version counts: %+v", list.Items)
	}
	if list.Items[0].LatestVersion == nil || list.Items[0].LatestVersion.Id != third.Version.Id {
		t.Fatalf("expected latest_version to be newest upload, got %+v", list.Items[0].LatestVersion)
	}
	if list.Items[1].LatestVersion == nil || list.Items[1].LatestVersion.Id != second.Version.Id {
		t.Fatalf("unexpected latest_version for beta: %+v", list.Items[1].LatestVersion)
	}
}

func TestScreenshotServiceGet_ReturnsVersionsDesc(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	first := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))
	time.Sleep(2 * time.Millisecond)
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))
	time.Sleep(2 * time.Millisecond)
	third := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))

	detail, err := svc.screenshotService.Get(first.Screen.Id, user.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(detail.Versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(detail.Versions))
	}
	if detail.Versions[0].Id != third.Version.Id || detail.Versions[1].Id != second.Version.Id || detail.Versions[2].Id != first.Version.Id {
		t.Fatalf("expected uploaded_at desc order, got %+v", detail.Versions)
	}
}

func TestScreenshotServiceGet_ForeignScreenNotFound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	other := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, other.ID)

	result := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, other.ID, "login"))

	if _, err := svc.screenshotService.Get(result.Screen.Id, user.ID); err != errs.ErrScreenshotScreenNotFound {
		t.Fatalf("expected ErrScreenshotScreenNotFound for foreign screen, got %v", err)
	}
	if err := svc.screenshotService.DeleteScreen(result.Screen.Id, user.ID); err != errs.ErrScreenshotScreenNotFound {
		t.Fatalf("expected ErrScreenshotScreenNotFound on delete, got %v", err)
	}
	if _, _, _, err := svc.screenshotService.GetVersionContent(result.Version.Id, user.ID); err != errs.ErrScreenshotVersionNotFound {
		t.Fatalf("expected ErrScreenshotVersionNotFound for foreign version, got %v", err)
	}
}

func TestScreenshotServiceUpdate_PointerSemantics(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	input := newScreenshotUploadInput(project.ID, user.ID, "login")
	input.Group = api.Ptr("auth")
	input.Title = "登录"
	uploaded := uploadTestScreenshot(t, svc, input)

	// 仅 group（空串→未分组），title 不变
	updated, err := svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Group: api.Ptr(""),
	})
	if err != nil {
		t.Fatalf("update group: %v", err)
	}
	if updated.Group != "" || updated.Title != "登录" {
		t.Fatalf("expected group cleared and title preserved, got %+v", updated)
	}
	if len(updated.Versions) != 1 {
		t.Fatalf("expected detail shape with versions, got %+v", updated)
	}

	// 仅 title
	updated, err = svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{
		Title: api.Ptr("新标题"),
	})
	if err != nil {
		t.Fatalf("update title: %v", err)
	}
	if updated.Title != "新标题" || updated.Group != "" {
		t.Fatalf("expected title updated only, got %+v", updated)
	}

	// 都不传 → 40001
	if _, err := svc.screenshotService.Update(uploaded.Screen.Id, user.ID, &UpdateScreenshotScreenRequest{}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty patch, got %v", err)
	}
}

func TestScreenshotServiceDeleteVersion_DeletesScreenWhenLast(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	first := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))
	time.Sleep(2 * time.Millisecond)
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))

	secondPath := mustScreenshotVersionPath(t, svc, second.Version.Id)

	if err := svc.screenshotService.DeleteVersion(second.Version.Id, user.ID); err != nil {
		t.Fatalf("delete version: %v", err)
	}
	screen, err := svc.screenshotRepo.FindScreenByID(first.Screen.Id)
	if err != nil {
		t.Fatalf("expected screen to survive with one version left, got %v", err)
	}
	if screen.VersionCount != 1 {
		t.Fatalf("expected version_count 1, got %d", screen.VersionCount)
	}
	if exists, _ := svc.storage.Exists(secondPath); exists {
		t.Fatalf("expected deleted version file removed")
	}

	firstPath := mustScreenshotVersionPath(t, svc, first.Version.Id)
	if err := svc.screenshotService.DeleteVersion(first.Version.Id, user.ID); err != nil {
		t.Fatalf("delete last version: %v", err)
	}
	if _, err := svc.screenshotRepo.FindScreenByID(first.Screen.Id); err == nil {
		t.Fatalf("expected screen removed with last version")
	}
	if exists, _ := svc.storage.Exists(firstPath); exists {
		t.Fatalf("expected last version file removed")
	}
}

func TestScreenshotServiceDeleteScreen_RemovesRowsAndFiles(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	first := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))
	second := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))

	firstPath := mustScreenshotVersionPath(t, svc, first.Version.Id)
	secondPath := mustScreenshotVersionPath(t, svc, second.Version.Id)

	if err := svc.screenshotService.DeleteScreen(first.Screen.Id, user.ID); err != nil {
		t.Fatalf("delete screen: %v", err)
	}

	var count int64
	if err := svc.db.Model(&model.ScreenshotVersion{}).Where("screen_id = ?", first.Screen.Id).Count(&count).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected all version rows deleted, got %d", count)
	}
	if _, err := svc.screenshotRepo.FindScreenByID(first.Screen.Id); err == nil {
		t.Fatalf("expected screen row deleted")
	}
	for _, path := range []string{firstPath, secondPath} {
		if exists, _ := svc.storage.Exists(path); exists {
			t.Fatalf("expected file %q removed", path)
		}
	}
}

func TestScreenshotServiceGetVersionContent_ReturnsStoredBytes(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	result := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))

	reader, mimeType, fileSize, err := svc.screenshotService.GetVersionContent(result.Version.Id, user.ID)
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

func mustScreenshotVersionPath(t *testing.T, svc *testServices, versionID string) string {
	t.Helper()
	version, err := svc.screenshotRepo.FindVersionByID(versionID)
	if err != nil {
		t.Fatalf("find version %q: %v", versionID, err)
	}
	return version.FilePath
}
