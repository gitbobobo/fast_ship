package router

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/handler"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type routerTestEnv struct {
	router     *gin.Engine
	db         *gorm.DB
	apiKeyRepo *repository.ApiKeyRepository
}

type routerEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type routerConfigOption func(*config.Config)

var routerTestPNGBytes = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
}

func TestRouterAuthRegisterLoginAndMe(t *testing.T) {
	env := setupRouterTestEnv(t)

	registerBody := []byte(`{"username":"tester","email":"tester@example.com","password":"Password123"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")

	registerRec := httptest.NewRecorder()
	env.router.ServeHTTP(registerRec, registerReq)

	if registerRec.Code != http.StatusOK {
		t.Fatalf("expected register 200, got %d: %s", registerRec.Code, registerRec.Body.String())
	}

	var registerResp struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	decodeRouterEnvelope(t, registerRec, &registerResp)
	if registerResp.Token == "" {
		t.Fatalf("expected token in register response")
	}

	loginBody := []byte(`{"login":"tester@example.com","password":"Password123"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")

	loginRec := httptest.NewRecorder()
	env.router.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d: %s", loginRec.Code, loginRec.Body.String())
	}

	var loginResp struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	decodeRouterEnvelope(t, loginRec, &loginResp)
	if loginResp.Token == "" {
		t.Fatalf("expected token in login response")
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+loginResp.Token)
	meRec := httptest.NewRecorder()
	env.router.ServeHTTP(meRec, meReq)

	if meRec.Code != http.StatusOK {
		t.Fatalf("expected me 200, got %d: %s", meRec.Code, meRec.Body.String())
	}

	var meResp struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	decodeRouterEnvelope(t, meRec, &meResp)
	if meResp.ID != registerResp.User.ID || meResp.Username != "tester" {
		t.Fatalf("unexpected /me response: %+v", meResp)
	}
}

func TestRouterShipCheckRequiresJWT(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shiptester", "shiptester@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = []byte{}
	})
	version := createRouterTestVersion(t, env.db, project.ID, func(v *model.Version) {
		v.ReleaseNotes = ""
		v.TargetCommitish = ""
	})

	rawKey := "RAWAPIKEY1234567890"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    auth.UserID,
		Name:      "CI-Android",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	apiKeyReq := httptest.NewRequest(http.MethodGet, "/api/versions/"+version.ID+"/ship-check", nil)
	apiKeyReq.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	apiKeyRec := httptest.NewRecorder()
	env.router.ServeHTTP(apiKeyRec, apiKeyReq)

	if apiKeyRec.Code != http.StatusForbidden {
		t.Fatalf("expected API key request 403, got %d: %s", apiKeyRec.Code, apiKeyRec.Body.String())
	}

	jwtReq := httptest.NewRequest(http.MethodGet, "/api/versions/"+version.ID+"/ship-check", nil)
	jwtReq.Header.Set("Authorization", "Bearer "+auth.Token)
	jwtRec := httptest.NewRecorder()
	env.router.ServeHTTP(jwtRec, jwtReq)

	if jwtRec.Code != http.StatusOK {
		t.Fatalf("expected JWT request 200, got %d: %s", jwtRec.Code, jwtRec.Body.String())
	}

	var resp struct {
		CanShip bool `json:"can_ship"`
		Items   []struct {
			Key string `json:"key"`
			OK  bool   `json:"ok"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, jwtRec, &resp)
	if resp.CanShip {
		t.Fatalf("expected can_ship=false")
	}
	if len(resp.Items) != 4 {
		t.Fatalf("expected 4 check items, got %d", len(resp.Items))
	}
}

func TestRouterArtifactUploadAndDownloadWithAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	user := createRouterTestUser(t, env.db, "user-2", "builder", "builder@example.com")
	project := createRouterTestProject(t, env.db, user.ID)
	version := createRouterTestVersion(t, env.db, project.ID)

	rawKey := "RAWAPIKEY0987654321"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		Name:      "CI-Upload",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	uploadReq := newRouterMultipartRequest(t, "/api/versions/"+version.ID+"/artifacts", "file", "app.apk", []byte("artifact-binary"), map[string]string{
		"platform": "android",
	})
	uploadReq.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))

	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var artifact struct {
		ID         string `json:"id"`
		UploadedBy string `json:"uploaded_by"`
		Platform   string `json:"platform"`
	}
	decodeRouterEnvelope(t, uploadRec, &artifact)
	if artifact.UploadedBy != "API Key: CI-Upload" {
		t.Fatalf("expected uploaded_by from API key, got %q", artifact.UploadedBy)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/download", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	downloadRec := httptest.NewRecorder()
	env.router.ServeHTTP(downloadRec, downloadReq)

	if downloadRec.Code != http.StatusOK {
		t.Fatalf("expected download 200, got %d: %s", downloadRec.Code, downloadRec.Body.String())
	}
	if downloadRec.Body.String() != "artifact-binary" {
		t.Fatalf("unexpected download body %q", downloadRec.Body.String())
	}
	if disposition := downloadRec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "app.apk") {
		t.Fatalf("expected filename in content disposition, got %q", disposition)
	}
}

func TestRouterArtifactDownloadWithQueryToken(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "artifactquery", "artifactquery@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	version := createRouterTestVersion(t, env.db, project.ID)

	uploadReq := newRouterMultipartRequest(t, "/api/versions/"+version.ID+"/artifacts", "file", "app.apk", []byte("artifact-query-token"), map[string]string{
		"platform": "android",
	})
	uploadReq.Header.Set("Authorization", "Bearer "+auth.Token)

	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var artifact struct {
		ID string `json:"id"`
	}
	decodeRouterEnvelope(t, uploadRec, &artifact)

	downloadReq := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/download?token="+auth.Token, nil)
	downloadRec := httptest.NewRecorder()
	env.router.ServeHTTP(downloadRec, downloadReq)

	if downloadRec.Code != http.StatusOK {
		t.Fatalf("expected download 200, got %d: %s", downloadRec.Code, downloadRec.Body.String())
	}
	if downloadRec.Body.String() != "artifact-query-token" {
		t.Fatalf("unexpected download body %q", downloadRec.Body.String())
	}
}

func TestRouterIssueAssetUploadAndContentWithQueryToken(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "issueasset", "issueasset@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	createReq := httptest.NewRequest(
		http.MethodPost,
		"/api/projects/"+project.ID+"/issues",
		bytes.NewReader([]byte(`{"title":"补充发布检查","body":"old body"}`)),
	)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+auth.Token)
	createRec := httptest.NewRecorder()
	env.router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("expected create issue 200, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var issue struct {
		ID string `json:"id"`
	}
	decodeRouterEnvelope(t, createRec, &issue)

	uploadReq := newRouterMultipartRequest(
		t,
		"/api/issues/"+issue.ID+"/assets",
		"file",
		"clip.png",
		routerTestPNGBytes,
		nil,
	)
	uploadReq.Header.Set("Authorization", "Bearer "+auth.Token)
	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var asset struct {
		ID         string `json:"id"`
		ContentURL string `json:"content_url"`
	}
	decodeRouterEnvelope(t, uploadRec, &asset)
	if asset.ID == "" || asset.ContentURL == "" {
		t.Fatalf("unexpected issue asset payload: %+v", asset)
	}

	contentReq := httptest.NewRequest(http.MethodGet, asset.ContentURL+"?token="+auth.Token, nil)
	contentRec := httptest.NewRecorder()
	env.router.ServeHTTP(contentRec, contentReq)
	if contentRec.Code != http.StatusOK {
		t.Fatalf("expected issue asset content 200, got %d: %s", contentRec.Code, contentRec.Body.String())
	}
	if string(contentRec.Body.Bytes()) != string(routerTestPNGBytes) {
		t.Fatalf("unexpected issue asset content length: %d", contentRec.Body.Len())
	}
}

func TestRouterDashboardOverview_ReturnsOpenIssueCountsByProject(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "dashboard-user", "dashboard@example.com", "Password123")

	projectA := createRouterTestProject(t, env.db, auth.UserID, func(project *model.Project) {
		project.Name = "iOS App"
	})
	projectB := createRouterTestProject(t, env.db, auth.UserID, func(project *model.Project) {
		project.Name = "Android App"
	})
	otherUser := createRouterTestUser(t, env.db, "other-user", "other-user", "other@example.com")
	otherProject := createRouterTestProject(t, env.db, otherUser.ID, func(project *model.Project) {
		project.Name = "Hidden Project"
	})

	createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateOpen
	})
	issueA2 := createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 2
		issue.State = model.IssueStateOpen
	})
	createRouterTestInternalMeta(t, env.db, issueA2.ID, func(meta *model.IssueInternalMeta) {
		meta.WorkflowStatus = model.IssueWorkflowStatusInProgress
	})
	issueA3 := createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 3
		issue.State = model.IssueStateOpen
	})
	createRouterTestInternalMeta(t, env.db, issueA3.ID, func(meta *model.IssueInternalMeta) {
		completedAt := time.Now().UTC().Add(-24 * time.Hour)
		meta.WorkflowStatus = model.IssueWorkflowStatusDone
		meta.CompletedAt = &completedAt
	})
	createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 4
		issue.State = model.IssueStateClosed
	})

	createRouterTestIssue(t, env.db, projectB.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateOpen
	})

	createRouterTestIssue(t, env.db, otherProject.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateOpen
	})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard overview 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp dashboardOverviewResponse
	decodeRouterEnvelope(t, rec, &resp)

	got := make(map[string]int, len(resp.OpenIssuesByProject))
	for _, item := range resp.OpenIssuesByProject {
		got[item.ProjectID] = item.OpenIssueCount
	}

	if len(got) != 2 {
		t.Fatalf("expected two visible projects in dashboard overview, got %+v", resp.OpenIssuesByProject)
	}
	if got[projectA.ID] != 2 {
		t.Fatalf("expected project %q to have 2 open issues, got %+v", projectA.Name, resp.OpenIssuesByProject)
	}
	if got[projectB.ID] != 1 {
		t.Fatalf("expected project %q to have 1 open issue, got %+v", projectB.Name, resp.OpenIssuesByProject)
	}
	if _, ok := got[otherProject.ID]; ok {
		t.Fatalf("expected dashboard overview to exclude other users' projects, got %+v", resp.OpenIssuesByProject)
	}
}

func TestRouterDashboardOverview_ReturnsDailyResolvedSeriesWithZeroFilledDates(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "dashboard-series", "dashboard-series@example.com", "Password123")

	projectA := createRouterTestProject(t, env.db, auth.UserID, func(project *model.Project) {
		project.Name = "iOS App"
	})
	projectB := createRouterTestProject(t, env.db, auth.UserID, func(project *model.Project) {
		project.Name = "Android App"
	})
	otherUser := createRouterTestUser(t, env.db, "series-other-user", "series-other-user", "series-other@example.com")
	otherProject := createRouterTestProject(t, env.db, otherUser.ID)

	dayStart := time.Now().UTC().Truncate(24 * time.Hour)
	twoDaysAgo := dayStart.AddDate(0, 0, -2).Add(12 * time.Hour)
	fiveDaysAgo := dayStart.AddDate(0, 0, -5).Add(9 * time.Hour)
	oldCompleted := dayStart.AddDate(0, 0, -35).Add(8 * time.Hour)

	issueA1 := createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateClosed
	})
	createRouterTestInternalMeta(t, env.db, issueA1.ID, func(meta *model.IssueInternalMeta) {
		meta.WorkflowStatus = model.IssueWorkflowStatusDone
		meta.CompletedAt = &twoDaysAgo
	})

	issueA2 := createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 2
		issue.State = model.IssueStateClosed
	})
	createRouterTestInternalMeta(t, env.db, issueA2.ID, func(meta *model.IssueInternalMeta) {
		meta.WorkflowStatus = model.IssueWorkflowStatusDone
		meta.CompletedAt = &fiveDaysAgo
	})

	issueB1 := createRouterTestIssue(t, env.db, projectB.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateClosed
	})
	createRouterTestInternalMeta(t, env.db, issueB1.ID, func(meta *model.IssueInternalMeta) {
		meta.WorkflowStatus = model.IssueWorkflowStatusDone
		meta.CompletedAt = &twoDaysAgo
	})

	issueOld := createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 3
		issue.State = model.IssueStateClosed
	})
	createRouterTestInternalMeta(t, env.db, issueOld.ID, func(meta *model.IssueInternalMeta) {
		meta.WorkflowStatus = model.IssueWorkflowStatusDone
		meta.CompletedAt = &oldCompleted
	})

	issueHidden := createRouterTestIssue(t, env.db, otherProject.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateClosed
	})
	createRouterTestInternalMeta(t, env.db, issueHidden.ID, func(meta *model.IssueInternalMeta) {
		meta.WorkflowStatus = model.IssueWorkflowStatusDone
		meta.CompletedAt = &twoDaysAgo
	})

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard overview 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp dashboardOverviewResponse
	decodeRouterEnvelope(t, rec, &resp)

	if len(resp.DailyResolved) != 30 {
		t.Fatalf("expected 30 daily resolved points, got %d", len(resp.DailyResolved))
	}

	wantCounts := map[string]int{
		dayStart.AddDate(0, 0, -5).Format("2006-01-02"): 1,
		dayStart.AddDate(0, 0, -2).Format("2006-01-02"): 2,
	}
	wantProjectCounts := map[string]map[string]int{
		dayStart.AddDate(0, 0, -5).Format("2006-01-02"): {projectA.ID: 1},
		dayStart.AddDate(0, 0, -2).Format("2006-01-02"): {projectA.ID: 1, projectB.ID: 1},
	}
	for index, point := range resp.DailyResolved {
		wantDate := dayStart.AddDate(0, 0, -(29 - index)).Format("2006-01-02")
		if point.Date != wantDate {
			t.Fatalf("expected daily resolved date %q at index %d, got %+v", wantDate, index, point)
		}
		wantCount := wantCounts[wantDate]
		if point.ResolvedCount != wantCount {
			t.Fatalf("expected resolved count %d for %s, got %+v", wantCount, wantDate, point)
		}
		wantProjects := wantProjectCounts[wantDate]
		gotProjects := make(map[string]int, len(point.Projects))
		for _, p := range point.Projects {
			gotProjects[p.ProjectID] = p.Count
		}
		for pid, wantCnt := range wantProjects {
			if gotProjects[pid] != wantCnt {
				t.Fatalf("expected project %s resolved count %d for %s, got %+v", pid, wantCnt, wantDate, point.Projects)
			}
		}
	}
}

func TestRouterDashboardOverview_ReturnsEmptyStateWhenUserHasNoProjects(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "dashboard-empty", "dashboard-empty@example.com", "Password123")

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard overview 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp dashboardOverviewResponse
	decodeRouterEnvelope(t, rec, &resp)

	if len(resp.OpenIssuesByProject) != 0 {
		t.Fatalf("expected empty project aggregation, got %+v", resp.OpenIssuesByProject)
	}
	if len(resp.DailyResolved) != 30 {
		t.Fatalf("expected empty state to still return 30 daily resolved points, got %d", len(resp.DailyResolved))
	}

	dayStart := time.Now().UTC().Truncate(24 * time.Hour)
	for index, point := range resp.DailyResolved {
		wantDate := dayStart.AddDate(0, 0, -(29 - index)).Format("2006-01-02")
		if point.Date != wantDate || point.ResolvedCount != 0 {
			t.Fatalf("expected zero-filled empty state point %q=0 at index %d, got %+v", wantDate, index, point)
		}
	}
}

func TestRouterServesSPAFromWebDist(t *testing.T) {
	webDistDir := t.TempDir()
	indexHTML := []byte("<!doctype html><html><body><div id=\"root\"></div></body></html>")
	assetJS := []byte("console.log('fast_ship')")

	if err := os.MkdirAll(filepath.Join(webDistDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDistDir, "index.html"), indexHTML, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDistDir, "assets", "app.js"), assetJS, 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	env := setupRouterTestEnv(t, func(cfg *config.Config) {
		cfg.Server.WebDistDir = webDistDir
	})

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRec := httptest.NewRecorder()
	env.router.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != http.StatusOK {
		t.Fatalf("expected root 200, got %d: %s", rootRec.Code, rootRec.Body.String())
	}
	if rootRec.Body.String() != string(indexHTML) {
		t.Fatalf("expected index html, got %q", rootRec.Body.String())
	}

	spaReq := httptest.NewRequest(http.MethodGet, "/projects/123", nil)
	spaRec := httptest.NewRecorder()
	env.router.ServeHTTP(spaRec, spaReq)
	if spaRec.Code != http.StatusOK {
		t.Fatalf("expected SPA route 200, got %d: %s", spaRec.Code, spaRec.Body.String())
	}
	if spaRec.Body.String() != string(indexHTML) {
		t.Fatalf("expected SPA fallback html, got %q", spaRec.Body.String())
	}

	assetReq := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	assetRec := httptest.NewRecorder()
	env.router.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("expected asset 200, got %d: %s", assetRec.Code, assetRec.Body.String())
	}
	if assetRec.Body.String() != string(assetJS) {
		t.Fatalf("expected asset body, got %q", assetRec.Body.String())
	}

	missingAssetReq := httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil)
	missingAssetRec := httptest.NewRecorder()
	env.router.ServeHTTP(missingAssetRec, missingAssetReq)
	if missingAssetRec.Code != http.StatusNotFound {
		t.Fatalf("expected missing asset 404, got %d", missingAssetRec.Code)
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/api/unknown", nil)
	apiRec := httptest.NewRecorder()
	env.router.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusNotFound {
		t.Fatalf("expected API 404, got %d", apiRec.Code)
	}
}

func setupRouterTestEnv(t *testing.T, opts ...routerConfigOption) *routerTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.UserAISetting{},
		&model.UserIssuePromptSetting{},
		&model.ApiKey{},
		&model.Project{},
		&model.Version{},
		&model.Issue{},
		&model.IssueGitHubMeta{},
		&model.IssueComment{},
		&model.IssueTimelineEvent{},
		&model.IssueInternalMeta{},
		&model.IssueChecklistItem{},
		&model.IssueSyncState{},
		&model.IssueAsset{},
		&model.IssueDraftAsset{},
		&model.Artifact{},
		&model.JWTBlacklist{},
		&model.RefreshToken{},
		&model.IssueCollabSuggestion{},
		&model.IssueCollabPlan{},
		&model.IssueCollabReview{},
		&model.IssueCollabSummary{},
		&model.LogBatch{},
		&model.LogEntry{},
		&model.Document{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_sequence ON issues(project_id, sequence_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_project_github_issue ON issue_github_meta(project_id, github_issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_issue_id ON issue_github_meta(issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_github_comment ON issue_comments(issue_id, github_comment_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_timeline_issue_event_key ON issue_timeline_events(issue_id, event_key)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_log_batches_project_run ON log_batches(project_id, run_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_documents_project_parent ON documents(project_id, parent_id)")

	cfg := &config.Config{
		Server: config.ServerConfig{},
		JWT: config.JWTConfig{
			Secret:      "test-secret",
			ExpireHours: 24,
		},
		Encryption: config.EncryptionConfig{
			Key: "12345678901234567890123456789012",
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	fileStorage := storage.NewLocalStorage(filepath.Join(t.TempDir(), "uploads"))

	userRepo := repository.NewUserRepository(db)
	userAISettingRepo := repository.NewUserAISettingRepository(db)
	userIssuePromptRepo := repository.NewUserIssuePromptSettingRepository(db)
	apiKeyRepo := repository.NewApiKeyRepository(db)
	dashboardRepo := repository.NewDashboardRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	issueGitHubMetaRepo := repository.NewIssueGitHubMetaRepository(db)
	issueCommentRepo := repository.NewIssueCommentRepository(db)
	issueTimelineRepo := repository.NewIssueTimelineRepository(db)
	issueInternalMetaRepo := repository.NewIssueInternalMetaRepository(db)
	issueChecklistRepo := repository.NewIssueChecklistRepository(db)
	issueSyncStateRepo := repository.NewIssueSyncStateRepository(db)
	issueAssetRepo := repository.NewIssueAssetRepository(db)
	issueDraftAssetRepo := repository.NewIssueDraftAssetRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)
	refreshTokenRepo := repository.NewRefreshTokenRepository(db)

	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg)
	aiService := service.NewAIService(userAISettingRepo, issueRepo, issueCommentRepo, projectRepo, cfg, zap.NewNop())
	issuePromptService := service.NewIssuePromptService(userIssuePromptRepo)
	apiKeyService := service.NewApiKeyService(apiKeyRepo)
	dashboardService := service.NewDashboardService(dashboardRepo)
	projectService := service.NewProjectService(projectRepo, versionRepo, issueSyncStateRepo, fileStorage, cfg)
	versionService := service.NewVersionService(versionRepo, projectRepo, fileStorage, cfg)
	githubRepoLabelRepo := repository.NewGitHubRepoLabelRepository(db)
	issueService := service.NewIssueService(issueRepo, issueGitHubMetaRepo, issueCommentRepo, issueTimelineRepo, issueInternalMetaRepo, issueChecklistRepo, issueSyncStateRepo, issueAssetRepo, issueDraftAssetRepo, projectRepo, userRepo, githubRepoLabelRepo, fileStorage, cfg, zap.NewNop())
	issueCollabRepo := repository.NewIssueCollabRepository(db)
	logRepo := repository.NewLogRepository(db)
	documentRepo := repository.NewDocumentRepository(db)
	issueCollabService := service.NewIssueCollabService(issueCollabRepo, issueRepo, projectRepo, userRepo)
	logService := service.NewLogService(logRepo, projectRepo)
	documentService := service.NewDocumentService(documentRepo, projectRepo)
	artifactService := service.NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage)
	shipService := service.NewShipService(versionRepo, projectRepo, artifactRepo, fileStorage, cfg, zap.NewNop())
	mediaProxyService := githubmedia.NewProxyService(filepath.Join(t.TempDir(), "media-cache"))

	authHandler := handler.NewAuthHandler(authService, fileStorage, cfg)
	aiHandler := handler.NewAIHandler(aiService)
	issuePromptHandler := handler.NewIssuePromptHandler(issuePromptService)
	apiKeyHandler := handler.NewApiKeyHandler(apiKeyService)
	dashboardHandler := handler.NewDashboardHandler(dashboardService)
	projectHandler := handler.NewProjectHandler(projectService)
	versionHandler := handler.NewVersionHandler(versionService, shipService)
	issueHandler := handler.NewIssueHandler(issueService)
	issueCollabHandler := handler.NewIssueCollabHandler(issueCollabService)
	logHandler := handler.NewLogHandler(logService)
	documentHandler := handler.NewDocumentHandler(documentService)
	artifactHandler := handler.NewArtifactHandler(artifactService)
	mediaProxyHandler := handler.NewGitHubMediaProxyHandler(mediaProxyService)

	r := gin.New()
	Setup(r, cfg, authHandler, aiHandler, issuePromptHandler, apiKeyHandler, dashboardHandler, projectHandler, versionHandler, issueHandler, issueCollabHandler, logHandler, documentHandler, artifactHandler, mediaProxyHandler, authService, apiKeyRepo)

	return &routerTestEnv{
		router:     r,
		db:         db,
		apiKeyRepo: apiKeyRepo,
	}
}

func createRouterTestUser(t *testing.T, db *gorm.DB, id, username, email string) *model.User {
	t.Helper()
	user := &model.User{
		ID:           id,
		Username:     username,
		Email:        email,
		PasswordHash: "hashed",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func createRouterTestProject(t *testing.T, db *gorm.DB, userID string, opts ...func(*model.Project)) *model.Project {
	t.Helper()
	project := &model.Project{
		ID:                   uuid.NewString(),
		UserID:               userID,
		Name:                 "proj-" + uuid.NewString(),
		Description:          "test project",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("token"),
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}
	for _, opt := range opts {
		opt(project)
	}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func createRouterTestVersion(t *testing.T, db *gorm.DB, projectID string, opts ...func(*model.Version)) *model.Version {
	t.Helper()
	version := &model.Version{
		ID:              uuid.NewString(),
		ProjectID:       projectID,
		VersionNumber:   "v1.0.0",
		Status:          model.VersionStatusPending,
		ReleaseNotes:    "notes",
		TargetCommitish: "main",
		CreatedAt:       time.Now(),
	}
	for _, opt := range opts {
		opt(version)
	}
	if err := db.Create(version).Error; err != nil {
		t.Fatalf("create version: %v", err)
	}
	return version
}

func decodeRouterEnvelope(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var env routerEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Code != 0 {
		t.Fatalf("expected success envelope, got code=%d body=%s", env.Code, rec.Body.String())
	}
	if target != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, target); err != nil {
			t.Fatalf("decode envelope data: %v", err)
		}
	}
}

func TestMediaProxyRequiresAuth(t *testing.T) {
	env := setupRouterTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/github/media-proxy?url=bad", nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMediaProxyAllowsQueryToken(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "media-user", "media@example.com", "Password123")

	req := httptest.NewRequest(http.MethodGet, "/api/github/media-proxy?url=bad&token="+auth.Token, nil)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 after passing auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

type routerAuthResult struct {
	UserID string
	Token  string
}

type dashboardOverviewResponse struct {
	OpenIssuesByProject []dashboardProjectOpenIssuePoint `json:"open_issues_by_project"`
	DailyResolved       []dashboardDailyResolvedPoint    `json:"daily_resolved"`
}

type dashboardProjectOpenIssuePoint struct {
	ProjectID      string `json:"project_id"`
	ProjectName    string `json:"project_name"`
	OpenIssueCount int    `json:"open_issue_count"`
}

type dashboardDailyResolvedProjectPoint struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Count       int    `json:"count"`
}

type dashboardDailyResolvedPoint struct {
	Date          string                               `json:"date"`
	ResolvedCount int                                  `json:"resolved_count"`
	Projects      []dashboardDailyResolvedProjectPoint `json:"projects"`
}

func registerAndLoginRouterUser(t *testing.T, r *gin.Engine, username, email, password string) routerAuthResult {
	t.Helper()

	registerBody := []byte(`{"username":"` + username + `","email":"` + email + `","password":"` + password + `"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	r.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("register failed: %d %s", registerRec.Code, registerRec.Body.String())
	}

	var registerResp struct {
		Token string `json:"token"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeRouterEnvelope(t, registerRec, &registerResp)

	loginBody := []byte(`{"login":"` + email + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	decodeRouterEnvelope(t, rec, &resp)
	return routerAuthResult{
		UserID: registerResp.User.ID,
		Token:  resp.Token,
	}
}

func createRouterTestIssue(t *testing.T, db *gorm.DB, projectID string, opts ...func(*model.Issue)) *model.Issue {
	t.Helper()

	now := time.Now().UTC()
	issue := &model.Issue{
		ID:             uuid.NewString(),
		ProjectID:      projectID,
		Source:         model.IssueSourceGitHub,
		SequenceNumber: 1,
		State:          model.IssueStateOpen,
		Title:          "Crash on launch",
		Body:           "App crashes",
		AuthorLogin:    "alice",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-1 * time.Hour),
	}
	for _, opt := range opts {
		opt(issue)
	}
	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return issue
}

func createRouterTestInternalMeta(t *testing.T, db *gorm.DB, issueID string, opts ...func(*model.IssueInternalMeta)) *model.IssueInternalMeta {
	t.Helper()

	now := time.Now().UTC()
	meta := &model.IssueInternalMeta{
		IssueID:         issueID,
		WorkflowStatus:  model.IssueWorkflowStatusTodo,
		UpdatedByUserID: "test-user",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	for _, opt := range opts {
		opt(meta)
	}
	if err := db.Create(meta).Error; err != nil {
		t.Fatalf("create internal meta: %v", err)
	}
	return meta
}

func TestRouterIssueWritesAcceptAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	user := createRouterTestUser(t, env.db, "user-issue-write", "issuewriter", "issuewriter@example.com")
	project := createRouterTestProject(t, env.db, user.ID)
	issue := createRouterTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
		i.Title = "API Key 编辑问题"
	})

	rawKey := "ISSUEWRITEKEY1234567890"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		Name:      "CI-Writer",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	authHeader := "Bearer " + service.FormatApiKey(rawKey)

	doJSON := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec
	}

	// 1. internal-meta (workflow_status)
	rec := doJSON(http.MethodPut, "/api/issues/"+issue.ID+"/internal-meta", []byte(`{"workflow_status":"in_progress"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("internal-meta: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var metaResp struct {
		WorkflowStatus string `json:"workflow_status"`
	}
	decodeRouterEnvelope(t, rec, &metaResp)
	if metaResp.WorkflowStatus != "in_progress" {
		t.Fatalf("internal-meta: expected workflow_status=in_progress, got %q", metaResp.WorkflowStatus)
	}

	// 2. checklist
	rec = doJSON(http.MethodPut, "/api/issues/"+issue.ID+"/checklist", []byte(`{"items":[{"title":"步骤一","is_completed":false},{"title":"步骤二","is_completed":false}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("checklist: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. assets
	uploadReq := newRouterMultipartRequest(t, "/api/issues/"+issue.ID+"/assets", "file", "clip.png", routerTestPNGBytes, nil)
	uploadReq.Header.Set("Authorization", authHeader)
	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("assets: expected 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}
}

func TestRouterIssueWriteRejectsCrossUserAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	owner := createRouterTestUser(t, env.db, "user-issue-owner", "issueowner", "issueowner@example.com")
	intruder := createRouterTestUser(t, env.db, "user-issue-intruder", "issueintruder", "issueintruder@example.com")
	ownerProject := createRouterTestProject(t, env.db, owner.ID)
	issue := createRouterTestIssue(t, env.db, ownerProject.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	rawKey := "INTRUDERKEY1234567890123"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    intruder.ID,
		Name:      "CI-Intruder",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	authHeader := "Bearer " + service.FormatApiKey(rawKey)

	cases := []struct {
		name string
		req  *http.Request
	}{
		{"internal-meta", httptest.NewRequest(http.MethodPut, "/api/issues/"+issue.ID+"/internal-meta", bytes.NewReader([]byte(`{"workflow_status":"done"}`)))},
		{"checklist", httptest.NewRequest(http.MethodPut, "/api/issues/"+issue.ID+"/checklist", bytes.NewReader([]byte(`{"items":[]}`)))},
		{"assets", newRouterMultipartRequest(t, "/api/issues/"+issue.ID+"/assets", "file", "clip.png", routerTestPNGBytes, nil)},
		{"checklist-suggestions", httptest.NewRequest(http.MethodPost, "/api/issues/"+issue.ID+"/checklist-suggestions", nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Header.Set("Authorization", authHeader)
			if tc.req.Header.Get("Content-Type") == "" {
				tc.req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			env.router.ServeHTTP(rec, tc.req)
			// 跨用户访问他人 Issue -> projectRepo 按 user_id 过滤 NotFound -> 404；若 middleware 退回 RequireJWT 则会是 403。
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: expected 404 for cross-user API key, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRouterCollabWritesRequireApiKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "collab-jwt", "collabjwt@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)
	issue := createRouterTestIssue(t, env.db, project.ID)

	rawKey := "COLLABROUTERKEY12345678"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    auth.UserID,
		Name:      "CI-Collab",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	apiKeyAuth := "Bearer " + service.FormatApiKey(rawKey)
	jwtAuth := "Bearer " + auth.Token

	doReq := func(method, authHeader, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec
	}

	sugPath := "/api/issues/" + issue.ID + "/collab/suggestions"

	// JWT 写 → 403（仅限 API Key）
	if rec := doReq(http.MethodPut, jwtAuth, sugPath, []byte(`{"items":[{"body":"x"}]}`)); rec.Code != http.StatusForbidden {
		t.Fatalf("JWT write suggestions expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// API Key 写 → 200
	rec := doReq(http.MethodPut, apiKeyAuth, sugPath, []byte(`{"items":[{"body":"建议一"},{"body":"建议二"}]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("API key write suggestions expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// GET 两类凭证均可
	getRec := doReq(http.MethodGet, jwtAuth, "/api/issues/"+issue.ID+"/collab", nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET collab expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var area struct {
		Suggestions []struct {
			Body string `json:"body"`
		} `json:"suggestions"`
	}
	decodeRouterEnvelope(t, getRec, &area)
	if len(area.Suggestions) != 2 {
		t.Fatalf("expected 2 suggestions after write, got %d", len(area.Suggestions))
	}

	// 旧路由已移除 → 404
	if rec := doReq(http.MethodPost, apiKeyAuth, "/api/issues/"+issue.ID+"/collab/notes", []byte(`{"body":"x"}`)); rec.Code != http.StatusNotFound {
		t.Fatalf("legacy notes route expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodPost, apiKeyAuth, "/api/issues/"+issue.ID+"/collab/questions", []byte(`{"items":[]}`)); rec.Code != http.StatusNotFound {
		t.Fatalf("legacy questions route expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// JWT DELETE plan → 200（幂等，即使 plan 不存在）
	planPath := "/api/issues/" + issue.ID + "/collab/plan"
	if rec := doReq(http.MethodDelete, jwtAuth, planPath, nil); rec.Code != http.StatusOK {
		t.Fatalf("JWT delete plan expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{
		"/api/issues/" + issue.ID + "/collab",
		"/api/issues/" + issue.ID + "/collab/suggestions",
		"/api/issues/" + issue.ID + "/collab/review",
		"/api/issues/" + issue.ID + "/collab/summary",
	} {
		if rec := doReq(http.MethodDelete, apiKeyAuth, path, nil); rec.Code != http.StatusOK {
			t.Fatalf("API key DELETE %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func newRouterMultipartRequest(t *testing.T, target, fieldName, fileName string, content []byte, extra map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	for key, value := range extra {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestRouterIssueCreateCommentRejectsAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	user := createRouterTestUser(t, env.db, "user-comment", "commenter", "commenter@example.com")
	project := createRouterTestProject(t, env.db, user.ID)
	issue := createRouterTestIssue(t, env.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	rawKey := "COMMENTKEY1234567890123"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		Name:      "CI-Comment",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/issues/"+issue.ID+"/comments", bytes.NewReader([]byte(`{"body":"通过 API Key 评论"}`)))
	req.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for API Key comment, got %d: %s", rec.Code, rec.Body.String())
	}
	var envelope routerEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}
}

func TestRouterBatchCloseRejectsAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	user := createRouterTestUser(t, env.db, "user-batch-close", "batchclose", "batchclose@example.com")
	project := createRouterTestProject(t, env.db, user.ID)

	rawKey := "BATCHCLOSEKEY1234567890"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		Name:      "CI-Batch",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/issues/batch-close", nil)
	req.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for API Key batch-close, got %d: %s", rec.Code, rec.Body.String())
	}
	var envelope routerEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Code != errs.ErrApiKeyForbidden.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyForbidden.Code, envelope.Code)
	}
}

func TestRouterLogWritesRequireApiKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "log-jwt", "logjwt@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	rawKey := "LOGROUTERKEY1234567890"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    auth.UserID,
		Name:      "CI-Logs",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	apiKeyAuth := "Bearer " + service.FormatApiKey(rawKey)
	jwtAuth := "Bearer " + auth.Token

	uploadBody := []byte(`{"run_id":"run-1","source":"smux","description":"batch note","entries":[{"timestamp":"2026-06-29T12:00:00Z","level":"info","message":"hello"}]}`)
	uploadPath := "/api/projects/" + project.ID + "/logs"

	doReq := func(method, authHeader, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec
	}

	rec := doReq(http.MethodPost, jwtAuth, uploadPath, uploadBody)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("JWT upload expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	var forbidden routerEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &forbidden); err != nil {
		t.Fatalf("decode forbidden: %v", err)
	}
	if forbidden.Code != errs.ErrApiKeyRequired.Code {
		t.Fatalf("expected code %d, got %d", errs.ErrApiKeyRequired.Code, forbidden.Code)
	}

	if rec = doReq(http.MethodPost, apiKeyAuth, uploadPath, uploadBody); rec.Code != http.StatusOK {
		t.Fatalf("API key upload expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var uploadResult struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	}
	decodeRouterEnvelope(t, rec, &uploadResult)
	if uploadResult.Description != "batch note" {
		t.Fatalf("expected description in upload response, got %q", uploadResult.Description)
	}

	getRec := doReq(http.MethodGet, jwtAuth, "/api/log-batches/"+uploadResult.ID, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("JWT get batch expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var batchDetail struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	}
	decodeRouterEnvelope(t, getRec, &batchDetail)
	if batchDetail.Description != "batch note" {
		t.Fatalf("expected description in get batch, got %q", batchDetail.Description)
	}

	apiKeyGetRec := doReq(http.MethodGet, apiKeyAuth, "/api/log-batches/"+uploadResult.ID, nil)
	if apiKeyGetRec.Code != http.StatusOK {
		t.Fatalf("API key get batch expected 200, got %d: %s", apiKeyGetRec.Code, apiKeyGetRec.Body.String())
	}

	if rec := doReq(http.MethodGet, apiKeyAuth, "/api/log-batches/"+uuid.NewString(), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing batch expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	listPath := "/api/projects/" + project.ID + "/logs"
	if rec := doReq(http.MethodGet, jwtAuth, listPath, nil); rec.Code != http.StatusOK {
		t.Fatalf("JWT list expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodGet, apiKeyAuth, listPath, nil); rec.Code != http.StatusOK {
		t.Fatalf("API key list expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	delRec := doReq(http.MethodDelete, apiKeyAuth, "/api/log-batches/"+uploadResult.ID, nil)
	if delRec.Code != http.StatusOK {
		t.Fatalf("API key delete batch expected 200, got %d: %s", delRec.Code, delRec.Body.String())
	}

	clearRec := doReq(http.MethodPost, apiKeyAuth, uploadPath, uploadBody)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("re-upload expected 200, got %d: %s", clearRec.Code, clearRec.Body.String())
	}
	decodeRouterEnvelope(t, clearRec, &uploadResult)

	projectClearRec := doReq(http.MethodDelete, jwtAuth, "/api/projects/"+project.ID+"/logs", nil)
	if projectClearRec.Code != http.StatusOK {
		t.Fatalf("JWT clear project logs expected 200, got %d: %s", projectClearRec.Code, projectClearRec.Body.String())
	}
}

func TestRouterLogUploadRejectsCrossUserAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	owner := createRouterTestUser(t, env.db, "user-log-owner", "logowner", "logowner@example.com")
	intruder := createRouterTestUser(t, env.db, "user-log-intruder", "logintruder", "logintruder@example.com")
	project := createRouterTestProject(t, env.db, owner.ID)

	rawKey := "LOGINTRUDERKEY123456789"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    intruder.ID,
		Name:      "CI-Log-Intruder",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	body := []byte(`{"run_id":"run-x","entries":[{"timestamp":"2026-06-29T12:00:00Z","level":"info","message":"x"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/logs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-user upload, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRouterDocumentCRUDJWTAndAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "doc-user", "docuser@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	rawKey := "DOCROUTERKEY1234567890"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    auth.UserID,
		Name:      "CI-Docs",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	apiKeyAuth := "Bearer " + service.FormatApiKey(rawKey)
	jwtAuth := "Bearer " + auth.Token

	doReq := func(method, authHeader, path string, body []byte) *httptest.ResponseRecorder {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		return rec
	}

	if rec := doReq(http.MethodGet, "", "/api/projects/"+project.ID+"/documents", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list expected 401, got %d", rec.Code)
	}

	createBody := []byte(`{"title":"Root Doc","body":"md body"}`)
	rec := doReq(http.MethodPost, jwtAuth, "/api/projects/"+project.ID+"/documents", createBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("JWT create expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var root struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Body     string  `json:"body"`
		ParentID *string `json:"parent_id"`
	}
	decodeRouterEnvelope(t, rec, &root)
	if root.Title != "Root Doc" || root.Body != "md body" || root.ParentID != nil {
		t.Fatalf("unexpected root: %+v", root)
	}

	childBody := []byte(`{"title":"Child","parent_id":"` + root.ID + `"}`)
	rec = doReq(http.MethodPost, apiKeyAuth, "/api/projects/"+project.ID+"/documents", childBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("API key create child expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var child struct {
		ID       string  `json:"id"`
		ParentID *string `json:"parent_id"`
	}
	decodeRouterEnvelope(t, rec, &child)
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatalf("unexpected child: %+v", child)
	}

	listRec := doReq(http.MethodGet, apiKeyAuth, "/api/projects/"+project.ID+"/documents", nil)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	if strings.Contains(listRec.Body.String(), `"body"`) {
		t.Fatalf("list response should not include body key: %s", listRec.Body.String())
	}

	getRec := doReq(http.MethodGet, jwtAuth, "/api/documents/"+root.ID, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}

	updateBody := []byte(`{"title":"Root Updated","body":"","parent_id":null}`)
	moveBody := []byte(`{"parent_id":null}`)
	moveRec := doReq(http.MethodPut, apiKeyAuth, "/api/documents/"+child.ID, moveBody)
	if moveRec.Code != http.StatusOK {
		t.Fatalf("move to root expected 200, got %d: %s", moveRec.Code, moveRec.Body.String())
	}
	var moved struct {
		ParentID *string `json:"parent_id"`
	}
	decodeRouterEnvelope(t, moveRec, &moved)
	if moved.ParentID != nil {
		t.Fatalf("expected null parent, got %+v", moved.ParentID)
	}

	updateRec := doReq(http.MethodPut, jwtAuth, "/api/documents/"+root.ID, updateBody)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update expected 200, got %d: %s", updateRec.Code, updateRec.Body.String())
	}

	reattach := []byte(`{"parent_id":"` + root.ID + `"}`)
	if rec := doReq(http.MethodPut, jwtAuth, "/api/documents/"+child.ID, reattach); rec.Code != http.StatusOK {
		t.Fatalf("reattach expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	delRec := doReq(http.MethodDelete, apiKeyAuth, "/api/documents/"+root.ID, nil)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete expected 200, got %d: %s", delRec.Code, delRec.Body.String())
	}
	if rec := doReq(http.MethodGet, jwtAuth, "/api/documents/"+child.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cascaded child expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRouterDocumentRejectsOversizedBody(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "doc-big", "docbig@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	body := []byte(`{"title":"big","body":"` + strings.Repeat("a", 1<<20) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/documents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}
