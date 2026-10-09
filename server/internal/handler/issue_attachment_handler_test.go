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
	"github.com/godbobo/fast_ship/server/internal/model"
)

func newAttachmentContext(t *testing.T, rec *httptest.ResponseRecorder, req *http.Request, aid string, userID string) *gin.Context {
	t.Helper()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	if aid != "" {
		ctx.Params = ginParams("aid", aid)
	}
	if userID != "" {
		ctx.Set(middleware.ContextKeyUserID, userID)
	}
	return ctx
}

func TestIssueAttachmentHandlerUploadDownloadAndDelete(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	uploadContent := []byte("attachment-binary")
	req, _ := newMultipartUploadRequest(t, "/api/issues/"+issue.ID+"/attachments", "file", "notes.txt", uploadContent, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	ctx.Set(middleware.ContextKeyUserName, user.Username)

	env.attachmentHandler.Upload(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var attachment struct {
		ID          string `json:"id"`
		FileName    string `json:"file_name"`
		FileSize    int64  `json:"file_size"`
		MimeType    string `json:"mime_type"`
		Uploader    string `json:"uploader"`
		DownloadURL string `json:"download_url"`
	}
	decodeEnvelope(t, rec, &attachment)
	if attachment.Uploader != user.Username {
		t.Fatalf("expected uploader %q, got %q", user.Username, attachment.Uploader)
	}
	if attachment.FileName != "notes.txt" || attachment.FileSize != int64(len(uploadContent)) {
		t.Fatalf("unexpected attachment payload: %+v", attachment)
	}
	if !strings.HasPrefix(attachment.MimeType, "text/plain") {
		t.Fatalf("expected sniffed text/plain mime_type, got %q", attachment.MimeType)
	}
	if attachment.DownloadURL != "/api/attachments/"+attachment.ID+"/download" {
		t.Fatalf("unexpected download_url %q", attachment.DownloadURL)
	}

	downloadRec := httptest.NewRecorder()
	downloadCtx := newAttachmentContext(t, downloadRec, httptest.NewRequest(http.MethodGet, attachment.DownloadURL, nil), attachment.ID, user.ID)
	env.attachmentHandler.Download(downloadCtx)

	if downloadRec.Code != http.StatusOK {
		t.Fatalf("expected download 200, got %d: %s", downloadRec.Code, downloadRec.Body.String())
	}
	if body := downloadRec.Body.String(); body != string(uploadContent) {
		t.Fatalf("expected download content %q, got %q", string(uploadContent), body)
	}
	if disposition := downloadRec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "notes.txt") {
		t.Fatalf("expected content disposition to include filename, got %q", disposition)
	}

	deleteRec := httptest.NewRecorder()
	deleteCtx := newAttachmentContext(t, deleteRec, httptest.NewRequest(http.MethodDelete, "/api/attachments/"+attachment.ID, nil), attachment.ID, user.ID)
	env.attachmentHandler.Delete(deleteCtx)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete 200, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	goneRec := httptest.NewRecorder()
	goneCtx := newAttachmentContext(t, goneRec, httptest.NewRequest(http.MethodGet, attachment.DownloadURL, nil), attachment.ID, user.ID)
	env.attachmentHandler.Download(goneCtx)
	if goneRec.Code != http.StatusNotFound {
		t.Fatalf("expected download 404 after delete, got %d: %s", goneRec.Code, goneRec.Body.String())
	}
}

func TestIssueAttachmentHandlerUploadSanitizesFileName(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	upload := func(fileName string) struct {
		ID          string `json:"id"`
		FileName    string `json:"file_name"`
		DownloadURL string `json:"download_url"`
	} {
		t.Helper()
		req, _ := newMultipartUploadRequest(t, "/api/issues/"+issue.ID+"/attachments", "file", fileName, []byte("x"), nil)
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = req
		ctx.Params = ginParams("iid", issue.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
		ctx.Set(middleware.ContextKeyUserName, user.Username)
		env.attachmentHandler.Upload(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("upload %q: expected 200, got %d: %s", fileName, rec.Code, rec.Body.String())
		}
		var attachment struct {
			ID          string `json:"id"`
			FileName    string `json:"file_name"`
			DownloadURL string `json:"download_url"`
		}
		decodeEnvelope(t, rec, &attachment)
		return attachment
	}

	// 路径穿越被 basename 削平
	if got := upload("../../etc/passwd"); got.FileName != "passwd" {
		t.Fatalf("expected sanitized file_name passwd, got %q", got.FileName)
	}
	// 引号/分号被剔除
	if got := upload(`a"b.txt`); got.FileName != "ab.txt" {
		t.Fatalf("expected sanitized file_name ab.txt, got %q", got.FileName)
	}
	if got := upload(`a;b.txt`); got.FileName != "ab.txt" {
		t.Fatalf("expected sanitized file_name ab.txt, got %q", got.FileName)
	}
}

func TestIssueAttachmentHandlerDownloadDispositionsNonASCII(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	req, _ := newMultipartUploadRequest(t, "/api/issues/"+issue.ID+"/attachments", "file", "报告.pdf", []byte("x"), nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	ctx.Set(middleware.ContextKeyUserName, user.Username)
	env.attachmentHandler.Upload(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var attachment struct {
		ID          string `json:"id"`
		FileName    string `json:"file_name"`
		DownloadURL string `json:"download_url"`
	}
	decodeEnvelope(t, rec, &attachment)
	if attachment.FileName != "报告.pdf" {
		t.Fatalf("expected original file_name 报告.pdf, got %q", attachment.FileName)
	}

	downloadRec := httptest.NewRecorder()
	downloadCtx := newAttachmentContext(t, downloadRec, httptest.NewRequest(http.MethodGet, attachment.DownloadURL, nil), attachment.ID, user.ID)
	env.attachmentHandler.Download(downloadCtx)
	if downloadRec.Code != http.StatusOK {
		t.Fatalf("expected download 200, got %d: %s", downloadRec.Code, downloadRec.Body.String())
	}
	disposition := downloadRec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, `filename="`) {
		t.Fatalf("expected ascii fallback filename in disposition, got %q", disposition)
	}
	if !strings.Contains(disposition, "filename*=UTF-8''") || !strings.Contains(disposition, "%E6%8A%A5%E5%91%8A") {
		t.Fatalf("expected RFC 5987 filename* for non-ASCII name, got %q", disposition)
	}
}

func TestIssueAttachmentHandlerUploadRejectsOversizedBody(t *testing.T) {
	// 请求体超限时应在 multipart 解析阶段返回 413，而不是先落临时盘再被 service 拒
	h := NewIssueAttachmentHandler(nil, &config.Config{
		Upload: config.UploadConfig{MaxFileSize: 1 << 10},
	})
	rec := httptest.NewRecorder()
	req, _ := newMultipartUploadRequest(t, "/api/issues/i1/attachments", "file", "big.bin", bytes.Repeat([]byte("x"), 2<<20), nil)
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = ginParams("iid", "i1")

	h.Upload(ctx)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIssueAttachmentHandlerUploadRejectsGitHubIssue(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	req, _ := newMultipartUploadRequest(t, "/api/issues/"+issue.ID+"/attachments", "file", "notes.txt", []byte("x"), nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.attachmentHandler.Upload(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected upload 409, got %d: %s", rec.Code, rec.Body.String())
	}
	envelope := decodeEnvelope(t, rec, nil)
	if envelope.Code != 40908 {
		t.Fatalf("expected code 40908, got %d", envelope.Code)
	}
}
