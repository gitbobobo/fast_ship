package service

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 标注/crop 路径需要可解码的完整位图，现存的 testPNGBytes 只有文件头
func encodeTestImage(t *testing.T, w, h int, encode func(io.Writer, image.Image) error) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(20 + x*2), uint8(20 + y*2), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := encode(&buf, img); err != nil {
		t.Fatalf("encode test image: %v", err)
	}
	return buf.Bytes()
}

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	return encodeTestImage(t, w, h, func(w io.Writer, img image.Image) error {
		return png.Encode(w, img)
	})
}

func makeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	return encodeTestImage(t, w, h, func(w io.Writer, img image.Image) error {
		return jpeg.Encode(w, img, nil)
	})
}

func makeTestGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	return encodeTestImage(t, w, h, func(w io.Writer, img image.Image) error {
		return gif.Encode(w, img, nil)
	})
}

// 8x6 有损 WebP（cwebp 生成的最小测试图），x/image 只提供解码器无法现造
var testWebPBytes = []byte{
	0x52, 0x49, 0x46, 0x46, 0x4e, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50,
	0x56, 0x50, 0x38, 0x20, 0x42, 0x00, 0x00, 0x00, 0xf0, 0x01, 0x00, 0x9d,
	0x01, 0x2a, 0x08, 0x00, 0x06, 0x00, 0x02, 0x00, 0x34, 0x25, 0xb0, 0x02,
	0x74, 0xba, 0x00, 0x02, 0xb9, 0x7d, 0x5a, 0x68, 0x00, 0xfe, 0xf5, 0xf2,
	0x68, 0xbd, 0x7f, 0xef, 0xca, 0xc9, 0x60, 0x92, 0xcb, 0xd8, 0xff, 0xc7,
	0x62, 0x56, 0xdf, 0x9c, 0x5e, 0x77, 0x59, 0x1b, 0xca, 0xf3, 0xff, 0xf2,
	0xe5, 0x15, 0xc3, 0xc1, 0xd7, 0x87, 0x38, 0x7e, 0x77, 0xff, 0xa7, 0x08,
	0x00, 0x00,
}

func uploadImageScreenshot(t *testing.T, svc *testServices, projectID, userID, screenKey, fileName string, content []byte) *ScreenshotUploadResult {
	t.Helper()
	input := newScreenshotUploadInput(projectID, userID, screenKey)
	input.FileName = fileName
	input.Reader = bytes.NewReader(content)
	return uploadTestScreenshot(t, svc, input)
}

func createTestAnnotation(t *testing.T, svc *testServices, versionID, userID string, req *CreateScreenshotAnnotationRequest) *ScreenshotAnnotationResponse {
	t.Helper()
	resp, err := svc.annotationService.Create(versionID, userID, "Web 用户", req)
	if err != nil {
		t.Fatalf("create annotation: %v", err)
	}
	return resp
}

func validAnnotationRequest() *CreateScreenshotAnnotationRequest {
	return &CreateScreenshotAnnotationRequest{
		X: 0.1, Y: 0.2, Width: 0.4, Height: 0.4, Body: "按钮偏左",
	}
}

func TestScreenshotServiceUpload_RecordsImageDimensions(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	result := uploadImageScreenshot(t, svc, project.ID, user.ID, "login", "shot.png", makeTestPNG(t, 100, 50))
	if result.Version.Width != 100 || result.Version.Height != 50 {
		t.Fatalf("expected dims 100x50, got %dx%d", result.Version.Width, result.Version.Height)
	}
	version, err := svc.screenshotRepo.FindVersionByID(result.Version.Id)
	if err != nil {
		t.Fatalf("find version: %v", err)
	}
	if version.Width != 100 || version.Height != 50 {
		t.Fatalf("expected persisted dims 100x50, got %dx%d", version.Width, version.Height)
	}

	// 无法解码文件头的图片不阻塞上传，尺寸记 0
	headerOnly := uploadTestScreenshot(t, svc, newScreenshotUploadInput(project.ID, user.ID, "login"))
	if headerOnly.Version.Width != 0 || headerOnly.Version.Height != 0 {
		t.Fatalf("expected zero dims for undecodable upload, got %dx%d", headerOnly.Version.Width, headerOnly.Version.Height)
	}
}

func TestScreenshotAnnotationServiceCreate_AssemblesResponse(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, " Login ", "shot.png", makeTestPNG(t, 100, 50))

	resp := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, validAnnotationRequest())

	if resp.Status != api.Open || resp.Body != "按钮偏左" || resp.CreatedBy != "Web 用户" {
		t.Fatalf("unexpected annotation payload: %+v", resp)
	}
	if resp.ProjectId != project.ID || resp.ScreenId != uploaded.Screen.Id || resp.VersionId != uploaded.Version.Id {
		t.Fatalf("unexpected linkage: %+v", resp)
	}
	if resp.ScreenKey != "login" {
		t.Fatalf("expected normalized screen_key, got %q", resp.ScreenKey)
	}
	if resp.ImageUrl != "/api/screenshot-versions/"+uploaded.Version.Id+"/content" {
		t.Fatalf("unexpected image_url %q", resp.ImageUrl)
	}
	if resp.CropUrl != "/api/screenshot-annotations/"+resp.Id+"/crop" {
		t.Fatalf("unexpected crop_url %q", resp.CropUrl)
	}
	if !resp.IsLatestVersion || resp.ImageWidth != 100 || resp.ImageHeight != 50 {
		t.Fatalf("unexpected version dims/latest: %+v", resp)
	}
	if resp.PixelRect == nil || *resp.PixelRect != (api.ScreenshotAnnotationRect{X: 10, Y: 10, Width: 40, Height: 20}) {
		t.Fatalf("unexpected pixel_rect: %+v", resp.PixelRect)
	}
	if resp.IssueId != nil || resp.IssueReference != nil || resp.IssueTitle != nil {
		t.Fatalf("expected unlinked issue fields, got %+v", resp)
	}
	if resp.ResolvedAt != nil || resp.CreatedAt == "" || resp.UpdatedAt == "" {
		t.Fatalf("unexpected timestamps: %+v", resp)
	}

	row, err := svc.annotationRepo.FindByID(resp.Id)
	if err != nil {
		t.Fatalf("find annotation: %v", err)
	}
	if row.ProjectID != project.ID || row.ScreenID != uploaded.Screen.Id || row.Status != "open" {
		t.Fatalf("unexpected stored row: %+v", row)
	}
}

func TestScreenshotAnnotationServiceCreate_LinksIssue(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 50))

	req := validAnnotationRequest()
	req.IssueId = api.Ptr(issue.ID)
	resp := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, req)

	if resp.IssueId == nil || *resp.IssueId != issue.ID {
		t.Fatalf("expected issue_id %q, got %+v", issue.ID, resp.IssueId)
	}
	if resp.IssueReference == nil || *resp.IssueReference != "GH-42" {
		t.Fatalf("expected issue_reference GH-42, got %+v", resp.IssueReference)
	}
	if resp.IssueTitle == nil || *resp.IssueTitle != "Crash on launch" {
		t.Fatalf("expected issue_title, got %+v", resp.IssueTitle)
	}
}

func TestScreenshotAnnotationServiceCreate_Validation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	other := createTestUser(t, svc.db, "user-2")
	otherProject := createTestProject(t, svc.db, other.ID)
	otherIssue := createTestIssue(t, svc.db, otherProject.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 50))

	badRects := []*CreateScreenshotAnnotationRequest{
		{X: -0.1, Y: 0.2, Width: 0.4, Height: 0.4, Body: "x"},
		{X: 0.1, Y: -0.2, Width: 0.4, Height: 0.4, Body: "x"},
		{X: 0.1, Y: 0.2, Width: 0, Height: 0.4, Body: "x"},
		{X: 0.1, Y: 0.2, Width: 0.4, Height: -0.1, Body: "x"},
		{X: 0.7, Y: 0.2, Width: 0.4, Height: 0.4, Body: "x"},
		{X: 0.1, Y: 0.8, Width: 0.4, Height: 0.4, Body: "x"},
		{X: 0.1, Y: 0.2, Width: 1.5, Height: 0.4, Body: "x"},
	}
	for i, req := range badRects {
		if _, err := svc.annotationService.Create(uploaded.Version.Id, user.ID, "u", req); err != errs.ErrInvalidParams {
			t.Fatalf("bad rect %d: expected ErrInvalidParams, got %v", i, err)
		}
	}

	for _, body := range []string{"", strings.Repeat("a", maxScreenshotAnnotationBodyRunes+1)} {
		req := validAnnotationRequest()
		req.Body = body
		if _, err := svc.annotationService.Create(uploaded.Version.Id, user.ID, "u", req); err != errs.ErrInvalidParams {
			t.Fatalf("body len %d: expected ErrInvalidParams, got %v", len(body), err)
		}
	}

	req := validAnnotationRequest()
	req.IssueId = api.Ptr(otherIssue.ID)
	if _, err := svc.annotationService.Create(uploaded.Version.Id, user.ID, "u", req); err != errs.ErrInvalidParams {
		t.Fatalf("cross-project issue: expected ErrInvalidParams, got %v", err)
	}
	req.IssueId = api.Ptr("missing-issue")
	if _, err := svc.annotationService.Create(uploaded.Version.Id, user.ID, "u", req); err != errs.ErrInvalidParams {
		t.Fatalf("missing issue: expected ErrInvalidParams, got %v", err)
	}

	// 空串 issue_id = 不关联，合法
	req.IssueId = api.Ptr("")
	if _, err := svc.annotationService.Create(uploaded.Version.Id, user.ID, "u", req); err != nil {
		t.Fatalf("empty issue_id should be accepted, got %v", err)
	}
}

func TestScreenshotAnnotationServiceCreate_ForeignOrMissingVersion(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	other := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, other.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, other.ID, "home", "shot.png", makeTestPNG(t, 100, 50))

	if _, err := svc.annotationService.Create(uploaded.Version.Id, user.ID, "u", validAnnotationRequest()); err != errs.ErrScreenshotVersionNotFound {
		t.Fatalf("expected ErrScreenshotVersionNotFound for foreign version, got %v", err)
	}
	if _, err := svc.annotationService.Create("missing-version", user.ID, "u", validAnnotationRequest()); err != errs.ErrScreenshotVersionNotFound {
		t.Fatalf("expected ErrScreenshotVersionNotFound for missing version, got %v", err)
	}
}

func TestScreenshotAnnotationServiceList_FiltersAndOrdering(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	v1 := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "a.png", makeTestPNG(t, 100, 50))
	v2 := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "b.png", makeTestPNG(t, 100, 50))
	other := uploadImageScreenshot(t, svc, project.ID, user.ID, "login", "c.png", makeTestPNG(t, 100, 50))

	a1 := createTestAnnotation(t, svc, v1.Version.Id, user.ID, validAnnotationRequest())
	a2req := validAnnotationRequest()
	a2req.IssueId = api.Ptr(issue.ID)
	a2 := createTestAnnotation(t, svc, v2.Version.Id, user.ID, a2req)
	a3 := createTestAnnotation(t, svc, other.Version.Id, user.ID, validAnnotationRequest())
	if _, err := svc.annotationService.Update(a3.Id, user.ID, &UpdateScreenshotAnnotationRequest{Status: api.Ptr(api.Resolved)}, false); err != nil {
		t.Fatalf("resolve a3: %v", err)
	}

	list, err := svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 3 || list.Items[0].Id != a1.Id || list.Items[1].Id != a2.Id || list.Items[2].Id != a3.Id {
		t.Fatalf("expected creation-order list, got %+v", list.Items)
	}
	if list.Items[0].IsLatestVersion || !list.Items[1].IsLatestVersion || !list.Items[2].IsLatestVersion {
		t.Fatalf("unexpected is_latest_version flags: %+v", list.Items)
	}
	if list.Items[1].IssueReference == nil || *list.Items[1].IssueReference != "GH-42" {
		t.Fatalf("expected issue_reference on a2, got %+v", list.Items[1])
	}

	resolved, err := svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{Status: "resolved"})
	if err != nil || len(resolved.Items) != 1 || resolved.Items[0].Id != a3.Id {
		t.Fatalf("status filter: %+v err=%v", resolved, err)
	}
	byIssue, err := svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{IssueID: issue.ID})
	if err != nil || len(byIssue.Items) != 1 || byIssue.Items[0].Id != a2.Id {
		t.Fatalf("issue filter: %+v err=%v", byIssue, err)
	}
	byScreen, err := svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{ScreenID: v1.Screen.Id})
	if err != nil || len(byScreen.Items) != 2 {
		t.Fatalf("screen filter: %+v err=%v", byScreen, err)
	}
	combo, err := svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{Status: "open", IssueID: issue.ID, ScreenID: v1.Screen.Id})
	if err != nil || len(combo.Items) != 1 || combo.Items[0].Id != a2.Id {
		t.Fatalf("combo filter: %+v err=%v", combo, err)
	}
	if _, err := svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{Status: "bogus"}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for bad status, got %v", err)
	}
	otherUser := createTestUser(t, svc.db, "user-2")
	if _, err := svc.annotationService.List(project.ID, otherUser.ID, ScreenshotAnnotationListFilters{}); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for foreign project, got %v", err)
	}
}

func TestScreenshotAnnotationServiceList_LazyDimsFallback(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 50))

	createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, validAnnotationRequest())

	// 模拟存量版本：宽高未记录但文件还在 → 惰性解码仍能换算像素矩形
	if err := svc.db.Model(&model.ScreenshotVersion{}).Where("id = ?", uploaded.Version.Id).
		Updates(map[string]interface{}{"width": 0, "height": 0}).Error; err != nil {
		t.Fatalf("zero version dims: %v", err)
	}
	list, err := svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	item := list.Items[0]
	if item.ImageWidth != 100 || item.ImageHeight != 50 || item.PixelRect == nil {
		t.Fatalf("expected lazy-decoded dims, got %+v", item)
	}

	// 文件不可读且尺寸未记录 → pixel_rect=null，宽高为 0
	version, err := svc.screenshotRepo.FindVersionByID(uploaded.Version.Id)
	if err != nil {
		t.Fatalf("find version: %v", err)
	}
	if err := svc.storage.Delete(version.FilePath); err != nil {
		t.Fatalf("delete stored file: %v", err)
	}
	list, err = svc.annotationService.List(project.ID, user.ID, ScreenshotAnnotationListFilters{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	item = list.Items[0]
	if item.ImageWidth != 0 || item.ImageHeight != 0 || item.PixelRect != nil {
		t.Fatalf("expected unknown dims and null pixel_rect, got %+v", item)
	}
}

func TestScreenshotAnnotationServiceUpdate_PointerSemantics(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 50))
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, validAnnotationRequest())

	// 仅 body
	updated, err := svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{
		Body: api.Ptr("改成新文案"),
	}, false)
	if err != nil || updated.Body != "改成新文案" || updated.Status != api.Open {
		t.Fatalf("body update: %+v err=%v", updated, err)
	}

	// status → resolved 置 resolved_at
	updated, err = svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{
		Status: api.Ptr(api.Resolved),
	}, false)
	if err != nil || updated.Status != api.Resolved || updated.ResolvedAt == nil {
		t.Fatalf("resolve: %+v err=%v", updated, err)
	}

	// status → open 清空 resolved_at
	updated, err = svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{
		Status: api.Ptr(api.Open),
	}, false)
	if err != nil || updated.Status != api.Open || updated.ResolvedAt != nil {
		t.Fatalf("reopen: %+v err=%v", updated, err)
	}

	// issue_id 设置与空串解除
	updated, err = svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{
		IssueId: api.Ptr(issue.ID),
	}, false)
	if err != nil || updated.IssueId == nil || *updated.IssueId != issue.ID {
		t.Fatalf("link issue: %+v err=%v", updated, err)
	}
	updated, err = svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{
		IssueId: api.Ptr(""),
	}, false)
	if err != nil || updated.IssueId != nil || updated.IssueReference != nil {
		t.Fatalf("unlink issue: %+v err=%v", updated, err)
	}

	// 三字段都不传 → 40001；非法 status → 40001
	if _, err := svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{}, false); err != errs.ErrInvalidParams {
		t.Fatalf("empty patch: expected ErrInvalidParams, got %v", err)
	}
	badStatus := api.ScreenshotAnnotationStatus("bogus")
	if _, err := svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{Status: &badStatus}, false); err != errs.ErrInvalidParams {
		t.Fatalf("bad status: expected ErrInvalidParams, got %v", err)
	}
	if _, err := svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{Body: api.Ptr("")}, false); err != errs.ErrInvalidParams {
		t.Fatalf("empty body: expected ErrInvalidParams, got %v", err)
	}
}

func TestScreenshotAnnotationServiceUpdate_APIKeyRestriction(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 50))
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, validAnnotationRequest())

	// API Key 只允许整包 {status:"resolved"}
	resolved, err := svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{
		Status: api.Ptr(api.Resolved),
	}, true)
	if err != nil || resolved.Status != api.Resolved {
		t.Fatalf("api key resolve: %+v err=%v", resolved, err)
	}

	for name, req := range map[string]*UpdateScreenshotAnnotationRequest{
		"reopen":        {Status: api.Ptr(api.Open)},
		"body":          {Body: api.Ptr("x")},
		"issue":         {IssueId: api.Ptr("some-issue")},
		"resolved+body": {Status: api.Ptr(api.Resolved), Body: api.Ptr("x")},
	} {
		if _, err := svc.annotationService.Update(annotation.Id, user.ID, req, true); err != errs.ErrApiKeyForbidden {
			t.Fatalf("api key %s: expected ErrApiKeyForbidden, got %v", name, err)
		}
	}
}

func TestScreenshotAnnotationServiceUpdateAndDelete_ForeignOrMissing(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	other := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, other.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, other.ID, "home", "shot.png", makeTestPNG(t, 100, 50))
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, other.ID, validAnnotationRequest())

	// 跨用户一律按不存在返回
	if _, err := svc.annotationService.Update(annotation.Id, user.ID, &UpdateScreenshotAnnotationRequest{Body: api.Ptr("x")}, false); err != errs.ErrScreenshotAnnotationNotFound {
		t.Fatalf("foreign update: expected 40416, got %v", err)
	}
	if err := svc.annotationService.Delete(annotation.Id, user.ID); err != errs.ErrScreenshotAnnotationNotFound {
		t.Fatalf("foreign delete: expected 40416, got %v", err)
	}
	if _, err := svc.annotationService.Update("missing", user.ID, &UpdateScreenshotAnnotationRequest{Body: api.Ptr("x")}, false); err != errs.ErrScreenshotAnnotationNotFound {
		t.Fatalf("missing update: expected 40416, got %v", err)
	}

	if err := svc.annotationService.Delete(annotation.Id, other.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := svc.annotationService.Delete(annotation.Id, other.ID); err != errs.ErrScreenshotAnnotationNotFound {
		t.Fatalf("repeat delete: expected 40416, got %v", err)
	}
	if _, err := svc.annotationRepo.FindByID(annotation.Id); err == nil {
		t.Fatalf("expected annotation row deleted")
	}
}

func TestScreenshotAnnotationService_Cascades(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	count := func() int64 {
		var n int64
		if err := svc.db.Model(&model.ScreenshotAnnotation{}).Count(&n).Error; err != nil {
			t.Fatalf("count annotations: %v", err)
		}
		return n
	}

	// 删版本级联删标注
	u1 := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "a.png", makeTestPNG(t, 100, 50))
	u2 := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "b.png", makeTestPNG(t, 100, 50))
	createTestAnnotation(t, svc, u1.Version.Id, user.ID, validAnnotationRequest())
	createTestAnnotation(t, svc, u2.Version.Id, user.ID, validAnnotationRequest())
	if err := svc.screenshotService.DeleteVersion(u1.Version.Id, user.ID); err != nil {
		t.Fatalf("delete version: %v", err)
	}
	if count() != 1 {
		t.Fatalf("expected 1 annotation after version delete, got %d", count())
	}

	// 删界面级联删剩余标注
	if err := svc.screenshotService.DeleteScreen(u2.Screen.Id, user.ID); err != nil {
		t.Fatalf("delete screen: %v", err)
	}
	if count() != 0 {
		t.Fatalf("expected 0 annotations after screen delete, got %d", count())
	}

	// 删 Issue 行只解除关联：标注保留且 issue_id 置空（FK SET NULL）
	issue := createTestIssue(t, svc.db, project.ID)
	u3 := uploadImageScreenshot(t, svc, project.ID, user.ID, "login", "c.png", makeTestPNG(t, 100, 50))
	req := validAnnotationRequest()
	req.IssueId = api.Ptr(issue.ID)
	linked := createTestAnnotation(t, svc, u3.Version.Id, user.ID, req)

	if err := svc.db.Delete(&model.Issue{}, "id = ?", issue.ID).Error; err != nil {
		t.Fatalf("delete issue row: %v", err)
	}
	row, err := svc.annotationRepo.FindByID(linked.Id)
	if err != nil {
		t.Fatalf("expected annotation kept after issue delete, got %v", err)
	}
	if row.IssueID != nil {
		t.Fatalf("expected issue_id NULL after issue delete, got %v", *row.IssueID)
	}
}

func TestScreenshotAnnotationServiceCrop_OutputsPNGWithMargin(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 50))

	// 矩形 {10,10,40,20}，外边距 = max(8, round(20*0.15))=8 → 裁剪 {2,2,58,38}
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, validAnnotationRequest())
	crop := readCropPNG(t, svc, annotation.Id, user.ID)
	if got := crop.Bounds(); got.Dx() != 56 || got.Dy() != 36 {
		t.Fatalf("expected crop 56x36, got %v", got)
	}

	// 贴角标注外边距被图边界 clamp：{0,0,10,5} + 8 → {0,0,18,13}
	req := &CreateScreenshotAnnotationRequest{X: 0, Y: 0, Width: 0.1, Height: 0.1, Body: "角"}
	corner := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, req)
	crop = readCropPNG(t, svc, corner.Id, user.ID)
	if got := crop.Bounds(); got.Dx() != 18 || got.Dy() != 13 {
		t.Fatalf("expected clamped crop 18x13, got %v", got)
	}
}

func TestScreenshotAnnotationServiceCrop_AllFormats(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	cases := []struct {
		name     string
		fileName string
		content  []byte
	}{
		{"png", "a.png", makeTestPNG(t, 60, 40)},
		{"jpeg", "a.jpg", makeTestJPEG(t, 60, 40)},
		{"gif", "a.gif", makeTestGIF(t, 60, 40)},
		{"webp", "a.webp", testWebPBytes},
	}
	for _, tc := range cases {
		uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, tc.name, tc.fileName, tc.content)
		req := &CreateScreenshotAnnotationRequest{X: 0.25, Y: 0.25, Width: 0.5, Height: 0.5, Body: tc.name}
		annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, req)
		crop := readCropPNG(t, svc, annotation.Id, user.ID)
		if crop.Bounds().Empty() {
			t.Fatalf("%s: expected non-empty crop", tc.name)
		}
	}
}

func TestScreenshotAnnotationServiceCrop_ForeignOrMissing(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	other := createTestUser(t, svc.db, "user-2")
	project := createTestProject(t, svc.db, other.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, other.ID, "home", "shot.png", makeTestPNG(t, 100, 50))
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, other.ID, validAnnotationRequest())

	if _, _, err := svc.annotationService.Crop(annotation.Id, user.ID); err != errs.ErrScreenshotAnnotationNotFound {
		t.Fatalf("foreign crop: expected 40416, got %v", err)
	}
	if _, _, err := svc.annotationService.Crop("missing", user.ID); err != errs.ErrScreenshotAnnotationNotFound {
		t.Fatalf("missing crop: expected 40416, got %v", err)
	}
}

// readCropPNG 调 Crop 并解码头验证输出确实是 PNG，返回解码后的图。
func readCropPNG(t *testing.T, svc *testServices, annotationID, userID string) image.Image {
	t.Helper()
	reader, release, err := svc.annotationService.Crop(annotationID, userID)
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	defer release()
	img, err := png.Decode(reader)
	if err != nil {
		t.Fatalf("crop output is not PNG: %v", err)
	}
	return img
}

func TestIssueServiceGet_EmbedsScreenshotAnnotations(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)
	// 未关联标注的 issue 放另一个项目，避开 GitHub meta 的项目内唯一索引
	lonelyProject := createTestProject(t, svc.db, user.ID)
	lonely := createTestIssue(t, svc.db, lonelyProject.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 50))
	req := validAnnotationRequest()
	req.IssueId = api.Ptr(issue.ID)
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, req)

	resp, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if len(resp.ScreenshotAnnotations) != 1 || resp.ScreenshotAnnotations[0].Id != annotation.Id {
		t.Fatalf("expected embedded annotation %q, got %+v", annotation.Id, resp.ScreenshotAnnotations)
	}
	embedded := resp.ScreenshotAnnotations[0]
	if embedded.IssueReference == nil || *embedded.IssueReference != "GH-42" {
		t.Fatalf("expected issue_reference GH-42, got %+v", embedded.IssueReference)
	}
	if embedded.ImageWidth != 100 || embedded.PixelRect == nil {
		t.Fatalf("expected dims in embedded annotation, got %+v", embedded)
	}

	resp, err = svc.issueService.Get(lonely.ID, user.ID)
	if err != nil {
		t.Fatalf("get lonely issue: %v", err)
	}
	if resp.ScreenshotAnnotations != nil {
		t.Fatalf("expected screenshot_annotations omitted for unlinked issue, got %+v", resp.ScreenshotAnnotations)
	}
}

// fakePNGWithDims 只含声明了尺寸的 IHDR 头（无像素数据）：DecodeConfig 能读出
// 宽高，Decode 会失败——用来测解码前的像素预算，不需要真的分配 3.6GB。
func fakePNGWithDims(w, h int) []byte {
	sig := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // truecolor
	chunk := make([]byte, 4, 12+13)
	copy(chunk, []byte("IHDR"))
	chunk = append(chunk, ihdr...)
	crc := crc32.ChecksumIEEE(chunk[0 : 4+13])
	out := append(sig, []byte{0, 0, 0, 13}...)
	out = append(out, chunk...)
	return append(out, []byte{byte(crc >> 24), byte(crc >> 16), byte(crc >> 8), byte(crc)}...)
}

// makeOffsetGIF 造一张逻辑画布 100×100、首帧是 (40,40)-(60,60) 偏移子块的 GIF。
func makeOffsetGIF(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{
		color.RGBA{0, 0, 0, 255},
		color.RGBA{200, 40, 40, 255},
	}
	frame := image.NewPaletted(image.Rect(40, 40, 60, 60), palette)
	for i := range frame.Pix {
		frame.Pix[i] = 1
	}
	var buf bytes.Buffer
	err := gif.EncodeAll(&buf, &gif.GIF{
		Image:  []*image.Paletted{frame},
		Delay:  []int{0},
		Config: image.Config{ColorModel: palette, Width: 100, Height: 100},
	})
	if err != nil {
		t.Fatalf("encode offset gif: %v", err)
	}
	return buf.Bytes()
}

func TestScreenshotAnnotationServiceCrop_RejectsOversizedImage(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	// 33 字节的头声明 30000×30000（~3.6GB 解码内存），预算外直接拒绝
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "big.png", fakePNGWithDims(30000, 30000))
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, validAnnotationRequest())
	if _, _, err := svc.annotationService.Crop(annotation.Id, user.ID); err != errs.ErrScreenshotImageTooLarge {
		t.Fatalf("expected ErrScreenshotImageTooLarge, got %v", err)
	}
}

func TestScreenshotAnnotationServiceCrop_SubPixelRectKeepsPosition(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "shot.png", makeTestPNG(t, 100, 100))

	// 0.8→0.801 取整后塌成空矩形：要保证至少 1px 且不丢位置
	req := &CreateScreenshotAnnotationRequest{X: 0.8, Y: 0.8, Width: 0.001, Height: 0.001, Body: "细"}
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, req)
	if annotation.PixelRect == nil {
		t.Fatalf("expected pixel_rect, got nil")
	}
	pr := annotation.PixelRect
	if pr.X != 80 || pr.Y != 80 || pr.Width < 1 || pr.Height < 1 {
		t.Fatalf("expected pixel_rect ~(80,80,≥1,≥1), got %+v", pr)
	}
	// 裁剪区域 (80,80,1,1)+外边距 8 → 原图 (72,72) 起的 17×17；
	// encodeTestImage 在 (x,y) 写入 {20+2x, 20+2y, 128, 255}，用像素色验证位置
	crop := readCropPNG(t, svc, annotation.Id, user.ID)
	if got := crop.Bounds(); got.Dx() != 17 || got.Dy() != 17 {
		t.Fatalf("expected 17x17 crop, got %v", got)
	}
	if r, g, b, _ := crop.At(0, 0).RGBA(); r>>8 != 164 || g>>8 != 164 || b>>8 != 128 {
		t.Fatalf("expected crop origin pixel ~(164,164,128) from (72,72), got (%d,%d,%d)", r>>8, g>>8, b>>8)
	}
}

func TestScreenshotAnnotationServiceCrop_GIFOffsetFrame(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "a.gif", makeOffsetGIF(t))
	if uploaded.Version.Width != 100 || uploaded.Version.Height != 100 {
		t.Fatalf("expected canvas dims 100x100, got %dx%d", uploaded.Version.Width, uploaded.Version.Height)
	}

	// 标注正好框住偏移帧 (40,40)-(60,60)：按画布坐标换算后裁剪必须成功
	req := &CreateScreenshotAnnotationRequest{X: 0.4, Y: 0.4, Width: 0.2, Height: 0.2, Body: "帧"}
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, req)
	crop := readCropPNG(t, svc, annotation.Id, user.ID)
	// 像素矩形 20×20 + 外边距 8 → 36×36（原图 (32,32) 起）；帧内是红色、帧外透明
	if got := crop.Bounds(); got.Dx() != 36 || got.Dy() != 36 {
		t.Fatalf("expected 36x36 crop, got %v", got)
	}
	if r, g, b, a := crop.At(8, 8).RGBA(); r>>8 != 200 || g>>8 != 40 || b>>8 != 40 || a>>8 != 255 {
		t.Fatalf("expected frame pixel red at (8,8), got (%d,%d,%d,%d)", r>>8, g>>8, b>>8, a>>8)
	}
	if _, _, _, a := crop.At(0, 0).RGBA(); a != 0 {
		t.Fatalf("expected transparent canvas pixel at (0,0), got alpha=%d", a>>8)
	}
}

// makeJPEGWithOrientation 造一张带 EXIF Orientation 标记的 JPEG：左半红、右半蓝，
// 用像素颜色验证展示方向是否被应用。
func makeJPEGWithOrientation(t *testing.T, orientation, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	data := buf.Bytes()

	// TIFF（MM 大端）：magic 42，IFD0 偏移 8，一个条目 tag 0x0112 SHORT×1
	tiff := []byte{
		'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08,
		0x00, 0x01,
		0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01,
		0x00, byte(orientation), 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(payload) + 2
	app1 := append([]byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}, payload...)

	out := append([]byte{}, data[:2]...)
	out = append(out, app1...)
	return append(out, data[2:]...)
}

func TestScreenshotAnnotationServiceCrop_JPEGExifOrientation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	// 存储像素 4×2（左红右蓝），方向 6 = 展示时顺时针 90°：展示图 2×4、上半红
	uploaded := uploadImageScreenshot(t, svc, project.ID, user.ID, "home", "a.jpg", makeJPEGWithOrientation(t, 6, 4, 2))
	if uploaded.Version.Width != 2 || uploaded.Version.Height != 4 {
		t.Fatalf("expected oriented dims 2x4, got %dx%d", uploaded.Version.Width, uploaded.Version.Height)
	}

	// 展示坐标系下框住上半（红色区）：裁剪出来的图顶部必须是红
	req := &CreateScreenshotAnnotationRequest{X: 0, Y: 0, Width: 1, Height: 0.5, Body: "红"}
	annotation := createTestAnnotation(t, svc, uploaded.Version.Id, user.ID, req)
	crop := readCropPNG(t, svc, annotation.Id, user.ID)
	if got := crop.Bounds(); got.Dx() != 2 || got.Dy() != 4 {
		t.Fatalf("expected 2x4 crop, got %v", got)
	}
	if r, _, b, _ := crop.At(0, 0).RGBA(); r>>8 <= b>>8 {
		t.Fatalf("expected red-dominant pixel at top, got r=%d b=%d", r>>8, b>>8)
	}
}

// TestApplyJPEGExifOrientation 用 4×2 四象限图（TL 红 / TR 绿 / BL 蓝 / BR 白）
// 逐方向核对落点：方向 5/7 一个转置一个反转置，曾经写反过。
func TestApplyJPEGExifOrientation(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 200, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	white := color.RGBA{255, 255, 255, 255}

	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			switch {
			case x < 2 && y == 0:
				src.Set(x, y, red)
			case x >= 2 && y == 0:
				src.Set(x, y, green)
			case x < 2:
				src.Set(x, y, blue)
			default:
				src.Set(x, y, white)
			}
		}
	}

	cases := []struct {
		orientation    int
		w, h           int
		tl, tr, bl, br color.RGBA
	}{
		{1, 4, 2, red, green, blue, white},
		{2, 4, 2, green, red, white, blue},
		{3, 4, 2, white, blue, green, red},
		{4, 4, 2, blue, white, red, green},
		{5, 2, 4, red, blue, green, white},
		{6, 2, 4, blue, red, white, green},
		{7, 2, 4, white, green, blue, red},
		{8, 2, 4, green, white, red, blue},
	}
	for _, tc := range cases {
		got := applyJPEGExifOrientation(src, tc.orientation)
		b := got.Bounds()
		if b.Dx() != tc.w || b.Dy() != tc.h {
			t.Fatalf("o%d: expected %dx%d, got %v", tc.orientation, tc.w, tc.h, b)
		}
		check := func(x, y int, want color.RGBA, name string) {
			r, g, b2, a := got.At(x, y).RGBA()
			if uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b2>>8) != want.B || uint8(a>>8) != want.A {
				t.Fatalf("o%d %s: expected %+v, got (%d,%d,%d,%d)", tc.orientation, name, want, r>>8, g>>8, b2>>8, a>>8)
			}
		}
		check(0, 0, tc.tl, "tl")
		check(b.Dx()-1, 0, tc.tr, "tr")
		check(0, b.Dy()-1, tc.bl, "bl")
		check(b.Dx()-1, b.Dy()-1, tc.br, "br")
	}
}
