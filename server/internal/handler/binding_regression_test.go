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

// github_pr_token 与 clear_github_pr_token 互斥按「字段是否显式提供」判定：
// false/空串也算提供。若 handler 退回 string+omitempty 绑定，这两种组合会被
// NonEmpty/True 吞掉一个字段，变成静默替换/清除。
func TestProjectUpdate_PrTokenMutexByPresence(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-pr-mutex")
	project := createHandlerTestProject(t, env.db, user.ID)

	for _, body := range []string{
		`{"clear_github_pr_token":false,"github_pr_token":"v2"}`,
		`{"clear_github_pr_token":true,"github_pr_token":""}`,
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

// clear_github_pr_token=false 单独提供是合法无操作，不得误报 400。
func TestProjectUpdate_ClearPRTokenFalseAloneIsOK(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-pr-clear-false")
	project := createHandlerTestProject(t, env.db, user.ID)

	ctx, rec := newJSONContext(http.MethodPut, "/api/projects/"+project.ID, []byte(`{"clear_github_pr_token":false}`))
	ctx.Params = ginParams("id", project.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	env.projectHandler.Update(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for clear=false alone, got %d: %s", rec.Code, rec.Body.String())
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
