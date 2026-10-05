package handler

import (
	"net/http"
	"testing"

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
