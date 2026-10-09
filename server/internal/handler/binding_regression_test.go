package handler

import (
	"net/http"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

// 绑定语义回归：生成类型字段是 *string，不能对显式空串走 binding omitempty
// 的跳过语义；updateMeInput/updateProjectInput 手写结构独占绑定。

func TestUpdateMe_EmptyStringFieldsAreIgnored(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-update-me-empty")

	ctx, rec := newJSONContext(http.MethodPut, "/api/auth/me", []byte(`{"username":"","email":""}`))
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.authHandler.UpdateMe(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for explicit empty strings, got %d: %s", rec.Code, rec.Body.String())
	}
	var stored model.User
	if err := env.db.Where("id = ?", user.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.Username != user.Username || stored.Email != user.Email {
		t.Fatalf("expected fields unchanged, got username=%q email=%q", stored.Username, stored.Email)
	}
}

func TestUpdateMe_ShortUsernameStillRejected(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-update-me-short")

	ctx, rec := newJSONContext(http.MethodPut, "/api/auth/me", []byte(`{"username":"x"}`))
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.authHandler.UpdateMe(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for too-short username, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestProjectUpdate_EmptyNameIsIgnored(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-proj-update-empty")
	project := createHandlerTestProject(t, env.db, user.ID)

	ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID, []byte(`{"name":""}`))
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.projectHandler.Update(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for explicit empty name, got %d: %s", rec.Code, rec.Body.String())
	}
	var stored model.Project
	if err := env.db.Where("id = ?", project.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if stored.Name != project.Name {
		t.Fatalf("expected name unchanged %q, got %q", project.Name, stored.Name)
	}
}

// clear_github_pr_token 与 github_pr_token / pr_token_source_project_id 互斥
// 按「字段是否显式提供」判定：false/空串/null 都算提供。若 handler 退回
// string+omitempty 或指针绑定，这些组合会被 NonEmpty/True/nil 吞掉一个字段，
// 变成静默替换/清除。
func TestProjectUpdate_PrTokenMutexByPresence(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-pr-mutex")
	project := createHandlerTestProject(t, env.db, user.ID)

	for _, body := range []string{
		`{"clear_github_pr_token":false,"github_pr_token":"v2"}`,
		`{"clear_github_pr_token":true,"github_pr_token":""}`,
		`{"clear_github_pr_token":true,"github_pr_token":null}`,
		`{"clear_github_pr_token":null,"github_pr_token":"v2"}`,
		`{"clear_github_pr_token":true,"pr_token_source_project_id":"x"}`,
		`{"clear_github_pr_token":true,"pr_token_source_project_id":null}`,
		`{"clear_github_pr_token":null,"pr_token_source_project_id":"x"}`,
	} {
		ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID, []byte(body))
		ctx.Params = ginParams("id", project.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.projectHandler.Update(ctx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
		envelope := decodeEnvelope(t, rec, nil)
		if envelope.Code != 40001 {
			t.Fatalf("expected code 40001 for %s, got %d", body, envelope.Code)
		}
	}
}

// pr_token_source_kind 按 presence 语义判定：JSON key 出现即算提供，
// 提供则必须为 access/pr——显式 null、空串、其他值、类型错误均 40001；
// 且须与 pr_token_source_project_id 搭配（单独提供 40001）。
// kind 不参与既有互斥判定：clear+kind（无 source）落在 kind-alone/取值校验，
// clear+kind+source 由互斥命中。
func TestProjectCreate_PRTokenSourceKindValidation(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-create-pr-kind")

	for _, body := range []string{
		`{"name":"x","pr_token_source_kind":null}`,
		`{"name":"x","pr_token_source_kind":"access"}`,
		`{"name":"x","pr_token_source_kind":"bogus"}`,
		`{"name":"x","pr_token_source_kind":5}`,
		`{"name":"x","pr_token_source_kind":"","pr_token_source_project_id":"x"}`,
	} {
		ctx, rec := newJSONContext(http.MethodPost, "/api/projects", []byte(body))
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.projectHandler.Create(ctx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
		envelope := decodeEnvelope(t, rec, nil)
		if envelope.Code != 40001 {
			t.Fatalf("expected code 40001 for %s, got %d", body, envelope.Code)
		}
	}
}

func TestProjectUpdate_PRTokenSourceKindValidation(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-pr-kind")
	project := createHandlerTestProject(t, env.db, user.ID)

	for _, body := range []string{
		`{"pr_token_source_kind":null}`,
		`{"pr_token_source_kind":null,"pr_token_source_project_id":"x"}`,
		`{"pr_token_source_kind":"access"}`,
		`{"pr_token_source_kind":"pr"}`,
		`{"pr_token_source_kind":"bogus"}`,
		`{"pr_token_source_kind":5}`,
		`{"pr_token_source_kind":"","pr_token_source_project_id":"x"}`,
		`{"clear_github_pr_token":true,"pr_token_source_kind":null}`,
		`{"clear_github_pr_token":true,"pr_token_source_kind":"access"}`,
		`{"clear_github_pr_token":true,"pr_token_source_kind":"access","pr_token_source_project_id":"x"}`,
	} {
		ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID, []byte(body))
		ctx.Params = ginParams("id", project.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.projectHandler.Update(ctx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
		envelope := decodeEnvelope(t, rec, nil)
		if envelope.Code != 40001 {
			t.Fatalf("expected code 40001 for %s, got %d", body, envelope.Code)
		}
	}
}

// clear_github_pr_token=false 或显式 null 单独提供是合法无操作，不得误报 400。
func TestProjectUpdate_ClearPRTokenFalseAloneIsOK(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-pr-clear-false")
	project := createHandlerTestProject(t, env.db, user.ID)

	for _, body := range []string{
		`{"clear_github_pr_token":false}`,
		`{"clear_github_pr_token":null}`,
		`{"github_pr_token":null}`,
		`{"pr_token_source_project_id":null}`,
	} {
		ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID, []byte(body))
		ctx.Params = ginParams("id", project.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.projectHandler.Update(ctx)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
}

// BatchCloseDone 的 source 是可选枚举：显式 "" 与缺省等价（关全部来源），不能因指针化变成 400。
func TestBatchCloseDone_EmptySourceIsDefault(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-batch-close-empty")
	project := createHandlerTestProject(t, env.db, user.ID)

	ctx, rec := newJSONContext(http.MethodPost, "/api/projects/"+project.ID+"/issues/batch-close", []byte(`{"source":""}`))
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.issueHandler.BatchCloseDone(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for explicit empty source, got %d: %s", rec.Code, rec.Body.String())
	}
}

// description 三态（INT-67 回归）：缺省与显式 null 都保留现值，显式 ""
// 清空，非空替换；响应体与库内读回一致。string+api.NonEmpty 会把空串折成
// nil 使清空不可达，service 曾漏赋值则连非空替换都不生效。
func TestProjectUpdate_DescriptionSemantics(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-proj-desc")
	project := createHandlerTestProject(t, env.db, user.ID)

	put := func(t *testing.T, body string) (api.Project, model.Project) {
		t.Helper()
		ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID, []byte(body))
		ctx.Params = ginParams("id", project.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.projectHandler.Update(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
		var resp api.Project
		decodeEnvelope(t, rec, &resp)
		var stored model.Project
		if err := env.db.Where("id = ?", project.ID).First(&stored).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return resp, stored
	}

	resp, stored := put(t, `{"description":"新描述"}`)
	if resp.Description != "新描述" || stored.Description != "新描述" {
		t.Fatalf("expected 新描述, got resp=%q stored=%q", resp.Description, stored.Description)
	}

	resp, stored = put(t, `{"description":""}`)
	if resp.Description != "" || stored.Description != "" {
		t.Fatalf("expected cleared description, got resp=%q stored=%q", resp.Description, stored.Description)
	}

	resp, stored = put(t, `{"description":"保留我"}`)
	if stored.Description != "保留我" {
		t.Fatalf("setup for preserve cases failed, stored=%q", stored.Description)
	}

	for _, body := range []string{
		`{}`,
		`{"description":null}`,
	} {
		resp, stored = put(t, body)
		if resp.Description != "保留我" || stored.Description != "保留我" {
			t.Fatalf("expected preserved 保留我 for %s, got resp=%q stored=%q", body, resp.Description, stored.Description)
		}
	}

	// 只改 name 不动 description
	resp, stored = put(t, `{"name":"renamed-proj"}`)
	if stored.Name != "renamed-proj" || resp.Name != "renamed-proj" {
		t.Fatalf("expected renamed name, got resp=%q stored=%q", resp.Name, stored.Name)
	}
	if resp.Description != "保留我" || stored.Description != "保留我" {
		t.Fatalf("expected description untouched, got resp=%q stored=%q", resp.Description, stored.Description)
	}
}

// 创建路径回归：新建时带描述，响应与库内读回一致（INT-67 中新建失效未复现，保留验证）。
func TestProjectCreate_DescriptionRoundTrip(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-proj-create-desc")

	ctx, rec := newJSONContext(http.MethodPost, "/api/projects", []byte(`{"name":"with-desc","description":"创建时的描述"}`))
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.projectHandler.Create(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.Project
	decodeEnvelope(t, rec, &resp)
	if resp.Description != "创建时的描述" {
		t.Fatalf("expected response description 创建时的描述, got %q", resp.Description)
	}
	var stored model.Project
	if err := env.db.Where("id = ?", resp.Id).First(&stored).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if stored.Description != "创建时的描述" {
		t.Fatalf("expected stored description 创建时的描述, got %q", stored.Description)
	}
}

// ReplaceIssueChecklist 的 items 是 required：缺 key 返回 400；显式空数组合法（清空清单）。
func TestReplaceChecklist_ItemsKeyRequired(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-checklist-req")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/checklist", []byte(`{}`))
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.issueHandler.ReplaceChecklist(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when items key missing, got %d: %s", rec.Code, rec.Body.String())
	}

	ctx, rec = newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/checklist", []byte(`{"items":[]}`))
	ctx.Params = ginParams("iid", issue.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.issueHandler.ReplaceChecklist(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty items array, got %d: %s", rec.Code, rec.Body.String())
	}
}
