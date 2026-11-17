package repository

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// GetIssueCounts：unset/todo/in_progress/done 四列与 open 总数在同一批
// 数据上的取值（INT-239）：SELECT 由普查模块片段拼成多占位符，本测试钉住
// 片段与 args 的成对顺序——加减谓词时错位的错误不能被静默计成别的桶。
// closed 行不计入任何 workflow 桶，meta 缺行与空串都归 unset。
func TestProjectGetIssueCountsBuckets(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)

	// 无 meta 行（unset 的一种）
	dashboardTestIssue(t, db, pid, "i-unset-missing")
	// 空串 meta
	emptyID := dashboardTestIssue(t, db, pid, "i-unset-empty")
	if err := db.Create(&model.IssueInternalMeta{IssueID: emptyID, WorkflowStatus: "", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create empty meta: %v", err)
	}
	mk := func(title string, status model.IssueWorkflowStatus, state model.IssueState) {
		iid := dashboardTestIssue(t, db, pid, title)
		if state == model.IssueStateClosed {
			if err := db.Model(&model.Issue{}).Where("id = ?", iid).Update("state", state).Error; err != nil {
				t.Fatalf("close issue: %v", err)
			}
		}
		if err := db.Create(&model.IssueInternalMeta{IssueID: iid, WorkflowStatus: status, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("create meta %s: %v", status, err)
		}
	}
	mk("i-todo", model.IssueWorkflowStatusTodo, model.IssueStateOpen)
	mk("i-progress", model.IssueWorkflowStatusInProgress, model.IssueStateOpen)
	mk("i-done", model.IssueWorkflowStatusDone, model.IssueStateOpen)
	mk("i-closed-todo", model.IssueWorkflowStatusTodo, model.IssueStateClosed) // closed 不计

	counts, err := NewProjectRepository(db).GetIssueCounts([]string{pid})
	if err != nil {
		t.Fatalf("GetIssueCounts: %v", err)
	}
	got := counts[pid]
	if got.Total != 6 {
		t.Fatalf("total=%d, want 6（含 closed）", got.Total)
	}
	if got.Unset != 2 || got.Todo != 1 || got.InProgress != 1 || got.Done != 1 {
		t.Fatalf("counts=%+v, want {unset:2 todo:1 in_progress:1 done:1}", got)
	}
}
