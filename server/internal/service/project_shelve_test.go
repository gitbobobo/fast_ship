package service

// 项目搁置（INT-165）service 层测试：Shelve/Unshelve 行为（无限期/过去
// until→400/到期响应字段/幂等取消）、ProjectService.List 的搁置过滤口径、
// 推荐汇总路径对搁置项目的排除（单项目查询不过滤）。

import (
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// shelveProject 直接经 service 搁置；返回更新后的响应供断言。
func shelveProject(t *testing.T, svc *ProjectService, id, userID string, until *string) *ProjectResponse {
	t.Helper()
	resp, err := svc.Shelve(id, userID, &api.ShelveProjectRequest{Until: until})
	if err != nil {
		t.Fatalf("shelve: %v", err)
	}
	return resp
}

func TestProjectServiceShelve_IndefiniteAndUntil(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "shelve-user")
	projectSvc := newTestProjectService(svc)
	active := createTestProject(t, svc.db, user.ID)

	// 空 body（req 为 nil 语义由 handler 保证，service 收 nil 同样按无限期）。
	resp, err := projectSvc.Shelve(active.ID, user.ID, nil)
	if err != nil {
		t.Fatalf("shelve without body: %v", err)
	}
	if resp.ShelvedAt == nil || resp.ShelvedUntil != nil || !resp.IsShelved {
		t.Fatalf("无限期搁置响应不符: %+v", resp)
	}

	// 未来 until：落库并回显；is_shelved 按响应时刻计算。
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	dated := createTestProject(t, svc.db, user.ID)
	resp = shelveProject(t, projectSvc, dated.ID, user.ID, &future)
	if resp.ShelvedAt == nil || resp.ShelvedUntil == nil || !resp.IsShelved {
		t.Fatalf("限期搁置响应不符: %+v", resp)
	}
	if parsed, err := time.Parse(time.RFC3339, *resp.ShelvedUntil); err != nil || !parsed.After(time.Now().UTC()) {
		t.Fatalf("shelved_until 应为未来时刻: %v %v", resp.ShelvedUntil, err)
	}

	// 重复搁置按覆盖处理：无限期覆盖限期后 until 清空。
	resp = shelveProject(t, projectSvc, dated.ID, user.ID, nil)
	if resp.ShelvedUntil != nil || !resp.IsShelved {
		t.Fatalf("重复搁置应覆盖 until: %+v", resp)
	}
}

func TestProjectServiceShelve_Validation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "shelve-invalid")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID)

	// 过去的 until 与非法 RFC3339 都是 40001。
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	for _, until := range []string{past, "not-a-time", ""} {
		_, err := projectSvc.Shelve(project.ID, user.ID, &api.ShelveProjectRequest{Until: &until})
		if err == nil {
			t.Fatalf("until=%q 应被拒绝", until)
		}
		if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
			t.Fatalf("until=%q 应返回 40001，实得 %v", until, err)
		}
	}
	// 拒绝后项目未被搁置。
	got, err := projectSvc.Get(project.ID, user.ID)
	if err != nil || got.IsShelved {
		t.Fatalf("拒绝的搁置不应生效: %+v %v", got, err)
	}

	// 项目不存在 / 非本人项目：40401。
	other := createTestUser(t, svc.db, "shelve-other")
	otherProject := createTestProject(t, svc.db, other.ID)
	if _, err := projectSvc.Shelve(uuid.NewString(), user.ID, nil); err != errs.ErrProjectNotFound {
		t.Fatalf("搁置不存在的项目应 40401，实得 %v", err)
	}
	if _, err := projectSvc.Shelve(otherProject.ID, user.ID, nil); err != errs.ErrProjectNotFound {
		t.Fatalf("搁置他人项目应 40401，实得 %v", err)
	}
}

func TestProjectServiceUnshelve_RestoresAndIdempotent(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "unshelve-user")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID)

	shelveProject(t, projectSvc, project.ID, user.ID, nil)

	// 取消搁置：两字段清空、is_shelved=false。
	resp, err := projectSvc.Unshelve(project.ID, user.ID)
	if err != nil {
		t.Fatalf("unshelve: %v", err)
	}
	if resp.ShelvedAt != nil || resp.ShelvedUntil != nil || resp.IsShelved {
		t.Fatalf("取消搁置后响应不符: %+v", resp)
	}

	// 未搁置再次取消是幂等成功；不存在的项目仍 404。
	if _, err := projectSvc.Unshelve(project.ID, user.ID); err != nil {
		t.Fatalf("未搁置的取消应幂等成功: %v", err)
	}
	if _, err := projectSvc.Unshelve(uuid.NewString(), user.ID); err != errs.ErrProjectNotFound {
		t.Fatalf("取消不存在的项目应 40401，实得 %v", err)
	}
}

// List 的过滤是 fs_project list 的默认口径：false 排除搁置中项目，true 返回
// 全部；已到期的搁置不排除（与 IsShelved 同一判定）。
func TestProjectServiceList_ShelvedFilter(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "list-shelve")
	projectSvc := newTestProjectService(svc)

	active := createTestProject(t, svc.db, user.ID)
	shelved := createTestProject(t, svc.db, user.ID)
	expired := createTestProject(t, svc.db, user.ID)

	shelveProject(t, projectSvc, shelved.ID, user.ID, nil)
	// 已到期搁置（截止拨到过去）：无定时任务，靠读取侧按 IsShelved 恢复。
	past := time.Now().UTC().Add(-time.Hour)
	if err := svc.db.Model(&model.Project{}).Where("id = ?", expired.ID).Updates(map[string]interface{}{
		"shelved_at":    past,
		"shelved_until": past,
	}).Error; err != nil {
		t.Fatalf("seed expired shelved: %v", err)
	}

	items, total, err := projectSvc.List(user.ID, 1, 20, false)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("默认口径应排除搁置中项目（保留未搁置与已到期），实得 total=%d len=%d", total, len(items))
	}
	got := map[string]bool{}
	for _, p := range items {
		got[p.Id] = true
	}
	if !got[active.ID] || !got[expired.ID] || got[shelved.ID] {
		t.Fatalf("过滤集合不符: %+v", got)
	}

	all, totalAll, err := projectSvc.List(user.ID, 1, 20, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if totalAll != 3 || len(all) != 3 {
		t.Fatalf("includeShelved=true 应返回全部项目，实得 total=%d len=%d", totalAll, len(all))
	}
}

// 推荐汇总（不带 project_id）排除搁置中项目；带 project_id 的单项目查询
// 照常返回搁置项目的推荐。
func TestIssueRecommendation_ListExcludesShelvedProjects(t *testing.T) {
	ts := setupTestServices(t)
	ownerID := uuid.NewString()
	createTestUser(t, ts.db, ownerID)
	active := createTestProject(t, ts.db, ownerID)
	shelved := createTestProject(t, ts.db, ownerID)

	activeIssue := createRecTestIssue(t, ts, active.ID)
	shelvedIssue := createRecTestIssue(t, ts, shelved.ID)
	for _, issue := range []*model.Issue{activeIssue, shelvedIssue} {
		if _, err := ts.recService.Upsert(issue.ID, ownerID, "bot-key", UpsertIssueRecommendationRequest{
			Reason: "搁置过滤测试 " + issue.ID,
		}); err != nil {
			t.Fatalf("upsert rec: %v", err)
		}
	}

	past := time.Now().UTC().Add(-time.Hour)
	if err := ts.db.Model(&model.Project{}).Where("id = ?", shelved.ID).Update("shelved_at", past).Error; err != nil {
		t.Fatalf("shelve project: %v", err)
	}

	summary, err := ts.recService.List(ownerID, "")
	if err != nil {
		t.Fatalf("summary list: %v", err)
	}
	if len(summary.Items) != 1 || summary.Items[0].Issue.Id != activeIssue.ID {
		t.Fatalf("汇总应只含未搁置项目的推荐: %+v", summary.Items)
	}

	single, err := ts.recService.List(ownerID, shelved.ID)
	if err != nil {
		t.Fatalf("single list: %v", err)
	}
	if len(single.Items) != 1 || single.Items[0].Issue.Id != shelvedIssue.ID {
		t.Fatalf("单项目查询应照常返回搁置项目的推荐: %+v", single.Items)
	}
}

// fs_project 的瘦身投影恒带搁置三字段，且不引入 REST 响应里的敏感引用。
func TestBotSlimProjectCarriesShelvedFields(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "slim-shelve")
	projectSvc := newTestProjectService(svc)
	project := createTestProject(t, svc.db, user.ID)

	shelved := shelveProject(t, projectSvc, project.ID, user.ID, nil)
	slim := botSlimProject(shelved)
	shelvedAt, _ := slim["shelved_at"].(*string)
	shelvedUntil, _ := slim["shelved_until"].(*string)
	isShelved, _ := slim["is_shelved"].(bool)
	if !isShelved || shelvedAt == nil || shelvedUntil != nil {
		t.Fatalf("瘦身投影应带搁置字段: %+v", slim)
	}
	raw, err := botMarshalResult(slim)
	if err != nil {
		t.Fatalf("marshal slim: %v", err)
	}
	if strings.Contains(raw, "public_github_token_id") || strings.Contains(raw, "code_github_token_id") {
		t.Fatalf("瘦身投影不得携带凭证引用: %s", raw)
	}

	restored, err := projectSvc.Unshelve(project.ID, user.ID)
	if err != nil {
		t.Fatalf("unshelve: %v", err)
	}
	slim = botSlimProject(restored)
	restoredAt, _ := slim["shelved_at"].(*string)
	restoredShelved, _ := slim["is_shelved"].(bool)
	if restoredShelved || restoredAt != nil {
		t.Fatalf("取消搁置后的瘦身投影应为未搁置: %+v", slim)
	}
}
