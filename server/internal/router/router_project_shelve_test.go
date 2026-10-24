package router

// 项目搁置（INT-165）REST 面测试：POST/DELETE /api/projects/:id/shelve 的
// 行为（无限期/限期/过去 until→400/幂等取消）、仅 JWT（API Key 403）、
// GET /api/projects 照常返回搁置项目、dashboard 概览排除搁置中项目。

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
)

type projectShelveResponse struct {
	ID           string  `json:"id"`
	ShelvedAt    *string `json:"shelved_at"`
	ShelvedUntil *string `json:"shelved_until"`
	IsShelved    bool    `json:"is_shelved"`
	Name         string  `json:"name"`
}

func callShelve(t *testing.T, env *routerTestEnv, token, method, projectID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/projects/"+projectID+"/shelve", nil)
	} else {
		req = httptest.NewRequest(method, "/api/projects/"+projectID+"/shelve", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	return rec
}

func TestRouterProjectShelveLifecycle(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shelve-user", "shelve@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	// 无 body 搁置：无限期（shelved_until=null、is_shelved=true）。
	rec := callShelve(t, env, auth.Token, http.MethodPost, project.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected shelve 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var shelved projectShelveResponse
	decodeRouterEnvelope(t, rec, &shelved)
	if shelved.ID != project.ID || shelved.ShelvedAt == nil || shelved.ShelvedUntil != nil || !shelved.IsShelved {
		t.Fatalf("unexpected indefinite shelve response: %+v", shelved)
	}

	// GET /api/projects/:id 照常返回搁置项目并带新字段；GET 列表同样返回。
	getReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+auth.Token)
	getRec := httptest.NewRecorder()
	env.router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected get 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var got projectShelveResponse
	decodeRouterEnvelope(t, getRec, &got)
	if !got.IsShelved || got.ShelvedAt == nil {
		t.Fatalf("expected shelved project in GET, got %+v", got)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	listReq.Header.Set("Authorization", "Bearer "+auth.Token)
	listRec := httptest.NewRecorder()
	env.router.ServeHTTP(listRec, listReq)
	var listResp struct {
		Items []projectShelveResponse `json:"items"`
		Total int                     `json:"total"`
	}
	decodeRouterEnvelope(t, listRec, &listResp)
	if listResp.Total != 1 || len(listResp.Items) != 1 || !listResp.Items[0].IsShelved {
		t.Fatalf("expected shelved project in list, got %+v", listResp)
	}

	// 未来 until 重复搁置按覆盖处理。
	until := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	rec = callShelve(t, env, auth.Token, http.MethodPost, project.ID, `{"until":"`+until+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected re-shelve 200, got %d: %s", rec.Code, rec.Body.String())
	}
	decodeRouterEnvelope(t, rec, &shelved)
	if shelved.ShelvedUntil == nil || !shelved.IsShelved {
		t.Fatalf("expected until set, got %+v", shelved)
	}

	// DELETE 取消搁置；未搁置再次取消幂等 200。
	for i := 0; i < 2; i++ {
		rec = callShelve(t, env, auth.Token, http.MethodDelete, project.ID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected unshelve 200 (call %d), got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	decodeRouterEnvelope(t, rec, &shelved)
	if shelved.ShelvedAt != nil || shelved.ShelvedUntil != nil || shelved.IsShelved {
		t.Fatalf("expected cleared shelve fields, got %+v", shelved)
	}
}

func TestRouterProjectShelveValidation(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shelve-bad", "shelvebad@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	// 过去的 until 与非法时间都是 400；项目未被搁置。
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	for _, body := range []string{`{"until":"` + past + `"}`, `{"until":"not-a-time"}`} {
		rec := callShelve(t, env, auth.Token, http.MethodPost, project.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	getReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+auth.Token)
	getRec := httptest.NewRecorder()
	env.router.ServeHTTP(getRec, getReq)
	var got projectShelveResponse
	decodeRouterEnvelope(t, getRec, &got)
	if got.IsShelved {
		t.Fatalf("rejected shelve must not take effect: %+v", got)
	}

	// 不存在的项目 404。
	rec := callShelve(t, env, auth.Token, http.MethodPost, uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown project, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 搁置是用户操作：API Key（Agent）不得改搁置状态，两类凭证都拦在路由层。
func TestRouterProjectShelveRequiresJWT(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shelvejwt", "shelvejwt@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID)

	rawKey := "RAWKEYSHELVE0001"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    auth.UserID,
		Name:      "CI-Shelve",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/projects/"+project.ID+"/shelve", nil)
		req.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
		rec := httptest.NewRecorder()
		env.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected API key %s shelve 403, got %d: %s", method, rec.Code, rec.Body.String())
		}
	}

	// JWT 照常可用。
	if rec := callShelve(t, env, auth.Token, http.MethodPost, project.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("expected JWT shelve 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// dashboard 概览的两组查询都排除搁置中项目：未完成数整行消失（不是归零），
// 每日完成趋势不再计入搁置项目的历史完成。
func TestRouterDashboardOverviewExcludesShelvedProjects(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shelve-dash", "shelvedash@example.com", "Password123")

	projectA := createRouterTestProject(t, env.db, auth.UserID, func(p *model.Project) {
		p.Name = "Active Project"
	})
	projectB := createRouterTestProject(t, env.db, auth.UserID, func(p *model.Project) {
		p.Name = "Shelved Project"
	})

	createRouterTestIssue(t, env.db, projectA.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateOpen
	})
	// projectB：一条 open（未完成数）与一条已完成（每日趋势），搁置后都应消失。
	createRouterTestIssue(t, env.db, projectB.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
		issue.State = model.IssueStateOpen
	})
	completed := createRouterTestIssue(t, env.db, projectB.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 2
		issue.State = model.IssueStateClosed
	})
	createRouterTestInternalMeta(t, env.db, completed.ID, func(meta *model.IssueInternalMeta) {
		completedAt := time.Now().UTC().Add(-24 * time.Hour)
		meta.WorkflowStatus = model.IssueWorkflowStatusDone
		meta.CompletedAt = &completedAt
	})

	// 无限期搁置 projectB。
	past := time.Now().UTC().Add(-time.Hour)
	if err := env.db.Model(&model.Project{}).Where("id = ?", projectB.ID).
		Update("shelved_at", past).Error; err != nil {
		t.Fatalf("shelve project: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/overview", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected dashboard overview 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp dashboardOverviewResponse
	decodeRouterEnvelope(t, rec, &resp)

	if len(resp.OpenIssuesByProject) != 1 || resp.OpenIssuesByProject[0].ProjectID != projectA.ID {
		t.Fatalf("expected only active project in overview, got %+v", resp.OpenIssuesByProject)
	}
	// 趋势窗口按天补零，搁置排除体现为计数归零。
	if total := sumDailyResolved(resp.DailyResolved); total != 0 {
		t.Fatalf("expected shelved project's completions excluded from trend, got %d", total)
	}

	// 取消搁置后恢复可见——到期恢复同口径（字段清空/过期即回到聚合面）。
	if err := env.db.Model(&model.Project{}).Where("id = ?", projectB.ID).
		Updates(map[string]interface{}{"shelved_at": nil, "shelved_until": nil}).Error; err != nil {
		t.Fatalf("unshelve project: %v", err)
	}
	rec = httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	decodeRouterEnvelope(t, rec, &resp)
	if len(resp.OpenIssuesByProject) != 2 {
		t.Fatalf("expected both projects after unshelve, got %+v", resp.OpenIssuesByProject)
	}
	if total := sumDailyResolved(resp.DailyResolved); total != 1 {
		t.Fatalf("expected trend to include unshelved project's completion, got %d", total)
	}
}

func sumDailyResolved(points []dashboardDailyResolvedPoint) int {
	sum := 0
	for _, p := range points {
		sum += p.ResolvedCount
	}
	return sum
}
