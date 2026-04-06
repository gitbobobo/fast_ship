package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

func TestArtifactHandlerUploadAndDownload(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	version := createHandlerTestVersion(t, env.db, project.ID)

	uploadContent := []byte("artifact-binary")
	req, _ := newMultipartUploadRequest(t, "/api/versions/"+version.ID+"/artifacts", "file", "app.apk", uploadContent, map[string]string{
		"platform": "android",
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = req
	ctx.Params = ginParams("vid", version.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	ctx.Set(middleware.ContextKeyUserName, user.Username)

	env.artifactHandler.Upload(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var artifact model.Artifact
	decodeEnvelope(t, rec, &artifact)
	if artifact.UploadedBy != user.Username {
		t.Fatalf("expected uploaded_by %q, got %q", user.Username, artifact.UploadedBy)
	}
	if artifact.Platform != "android" {
		t.Fatalf("expected platform android, got %q", artifact.Platform)
	}

	downloadRec := httptest.NewRecorder()
	downloadCtx, _ := gin.CreateTestContext(downloadRec)
	downloadCtx.Request = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/download", nil)
	downloadCtx.Params = ginParams("aid", artifact.ID)
	downloadCtx.Set(middleware.ContextKeyUserID, user.ID)

	env.artifactHandler.Download(downloadCtx)

	if downloadRec.Code != http.StatusOK {
		t.Fatalf("expected download 200, got %d: %s", downloadRec.Code, downloadRec.Body.String())
	}
	if body := downloadRec.Body.String(); body != string(uploadContent) {
		t.Fatalf("expected download content %q, got %q", string(uploadContent), body)
	}
	if disposition := downloadRec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "app.apk") {
		t.Fatalf("expected content disposition to include filename, got %q", disposition)
	}
}
