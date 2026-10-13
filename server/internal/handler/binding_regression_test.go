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

// Token 引用字段三态按 presence 判定：key 缺省保留现值；显式 null 与空串都
// 清空；非空替换。若 handler 退回 *string 指针绑定，null 与缺省不可区分，
// 清空语义会丢失——此组用例钉住该行为。
func TestProjectUpdate_TokenRefThreeStatesByPresence(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-ref-three")
	tokenA := createHandlerTestToken(t, env, user.ID, "ghp_token_a")
	tokenB := createHandlerTestToken(t, env, user.ID, "ghp_token_b")
	project := createHandlerTestProject(t, env.db, user.ID)

	put := func(t *testing.T, body string) api.Project {
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
		return resp
	}
	storedRefs := func(t *testing.T) (string, string) {
		t.Helper()
		var stored model.Project
		if err := env.db.Where("id = ?", project.ID).First(&stored).Error; err != nil {
			t.Fatalf("reload project: %v", err)
		}
		return stored.PublicGitHubTokenID, stored.CodeGitHubTokenID
	}

	// key 缺省：保留现值（auto token）
	resp := put(t, `{}`)
	if resp.PublicGithubTokenId == "" || resp.CodeGithubTokenId == "" {
		t.Fatalf("expected auto token refs preserved, got %q/%q", resp.PublicGithubTokenId, resp.CodeGithubTokenId)
	}

	// 非空替换
	put(t, `{"public_github_token_id":"`+tokenA.ID+`","code_github_token_id":"`+tokenB.ID+`"}`)
	if public, code := storedRefs(t); public != tokenA.ID || code != tokenB.ID {
		t.Fatalf("expected refs replaced, got %q/%q", public, code)
	}

	// 显式 null 清空；缺省 key 的另一用途保持
	put(t, `{"public_github_token_id":null}`)
	public, code := storedRefs(t)
	if public != "" || code != tokenB.ID {
		t.Fatalf("expected public cleared only, got %q/%q", public, code)
	}

	// 空串同样清空
	put(t, `{"code_github_token_id":""}`)
	public, code = storedRefs(t)
	if public != "" || code != "" {
		t.Fatalf("expected both cleared, got %q/%q", public, code)
	}
}

// Token 引用字段类型错误（数字/对象/数组）返回 40001；引用不属于当前用户
// 或不存在返回 40420；已下线的旧字段静默忽略、不再产生任何效果。
func TestProjectUpdate_TokenRefValidation(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-ref-validation")
	other := createHandlerTestUser(t, env.db, "user-ref-validation-other")
	foreign := createHandlerTestToken(t, env, other.ID, "ghp_foreign")
	project := createHandlerTestProject(t, env.db, user.ID)

	for _, body := range []string{
		`{"public_github_token_id":5}`,
		`{"code_github_token_id":{}}`,
		`{"public_github_token_id":["x"]}`,
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

	for _, body := range []string{
		`{"public_github_token_id":"nonexistent-token-id"}`,
		`{"code_github_token_id":"` + foreign.ID + `"}`,
	} {
		ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID, []byte(body))
		ctx.Params = ginParams("id", project.ID)
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.projectHandler.Update(ctx)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
		envelope := decodeEnvelope(t, rec, nil)
		if envelope.Code != 40420 {
			t.Fatalf("expected code 40420 for %s, got %d", body, envelope.Code)
		}
	}

	// 旧字段静默忽略：请求成功但不改动任何引用。
	ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID,
		[]byte(`{"github_token":"plaintext","github_pr_token":"plaintext","source_project_id":"x","pr_token_source_project_id":"y","pr_token_source_kind":"access","clear_github_pr_token":true}`))
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.projectHandler.Update(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy fields must be ignored, got %d: %s", rec.Code, rec.Body.String())
	}
	var stored model.Project
	if err := env.db.Where("id = ?", project.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if stored.GithubOwner != "owner" || stored.PublicGitHubTokenID == "" {
		t.Fatalf("legacy fields must not modify project state: %+v", stored)
	}
}

// 创建路径同样校验引用归属：40420。
func TestProjectCreate_TokenRefValidation(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-create-ref-validation")
	other := createHandlerTestUser(t, env.db, "user-create-ref-other")
	foreign := createHandlerTestToken(t, env, other.ID, "ghp_foreign_create")

	for _, body := range []string{
		`{"name":"ref-404","public_github_token_id":"nonexistent-token-id"}`,
		`{"name":"ref-404-foreign","code_github_token_id":"` + foreign.ID + `"}`,
	} {
		ctx, rec := newJSONContext(http.MethodPost, "/api/projects", []byte(body))
		ctx.Set(middleware.ContextKeyUserID, user.ID)
		env.projectHandler.Create(ctx)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
		envelope := decodeEnvelope(t, rec, nil)
		if envelope.Code != 40420 {
			t.Fatalf("expected code 40420 for %s, got %d", body, envelope.Code)
		}
	}

	// 只填仓库不填 Token 合法；响应原样返回引用（空串）。
	ctx, rec := newJSONContext(http.MethodPost, "/api/projects",
		[]byte(`{"name":"repo-only","repository_url":"https://github.com/owner/repo"}`))
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.projectHandler.Create(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for repo without token, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp api.Project
	decodeEnvelope(t, rec, &resp)
	if resp.GithubOwner != "owner" || resp.GithubRepo != "repo" || resp.PublicGithubTokenId != "" || resp.CodeGithubTokenId != "" {
		t.Fatalf("unexpected create response: %+v", resp)
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
