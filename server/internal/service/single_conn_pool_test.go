package service

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-97 回归：生产库 SetMaxOpenConns(1)，事务闭包内向池根再借一条连接会永久
// 等待（database/sql.(*DB).conn 无超时），整库冻结。这里把测试库压成同样的
// 单连接池形态，对三处历史漏点各做「goroutine + 超时」断言：修复前调用永不
// 返回（10s 超时判死锁），修复后秒过。
func limitPoolToSingleConn(t *testing.T, svc *testServices) {
	t.Helper()
	sqlDB, err := svc.db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
}

func completeWithin(t *testing.T, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s 10s 未返回：事务闭包内向池根借连接自锁（INT-97）", what)
	}
}

// PUT /api/issues/:iid/internal-meta：workflow_status 真实变化 + 无 author 的
// issue（GitHub 镜像恒为空）曾在事务内调 FindByIDGlobal 自锁。
func TestUpdateInternalMeta_SingleConnPool_NoAuthorIssue(t *testing.T) {
	svc := setupTestServices(t)
	limitPoolToSingleConn(t, svc)
	user := createTestUser(t, svc.db, "user-single-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID) // AuthorUserID 为空

	completeWithin(t, "UpdateInternalMeta", func() error {
		_, err := svc.issueService.UpdateInternalMeta(issue.ID, user.ID, model.IssueWorkflowStatusInProgress, "test")
		return err
	})

	meta, err := svc.internalMetaRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("get internal meta: %v", err)
	}
	if meta.WorkflowStatus != model.IssueWorkflowStatusInProgress {
		t.Fatalf("expected workflow_status=in_progress, got %q", meta.WorkflowStatus)
	}
}

// 无 author 的 internal issue 走 UpdateInternalIssue（存量/异常数据）：
// 事件归属解析曾在事务内借连接自锁。
func TestUpdateInternalIssue_SingleConnPool_NoAuthorIssue(t *testing.T) {
	svc := setupTestServices(t)
	limitPoolToSingleConn(t, svc)
	user := createTestUser(t, svc.db, "user-single-2")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID, func(i *model.Issue) {
		i.Source = model.IssueSourceInternal
	})

	closed := model.IssueStateClosed
	completeWithin(t, "UpdateInternalIssue", func() error {
		_, err := svc.issueService.UpdateInternalIssue(issue.ID, user.ID, UpdateInternalIssueRequest{State: &closed})
		return err
	})

	stored, err := svc.issueRepo.FindByID(issue.ID)
	if err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if stored.State != model.IssueStateClosed {
		t.Fatalf("expected state=closed, got %q", stored.State)
	}
}

// PUT /api/documents/:doc_id：事务内 ensureProjectAccess 走池根，任何已存在
// 文档的更新都会自锁。
func TestDocumentUpdate_SingleConnPool(t *testing.T) {
	docSvc, db, userID, projectID := setupDocumentServiceTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	doc, err := docSvc.Create(projectID, userID, &CreateDocumentRequest{Title: "doc"})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}

	title := "renamed"
	completeWithin(t, "DocumentUpdate", func() error {
		_, err := docSvc.Update(doc.Id, userID, &UpdateDocumentRequest{Title: &title})
		return err
	})

	stored, err := docSvc.Get(doc.Id, userID)
	if err != nil {
		t.Fatalf("get doc: %v", err)
	}
	if stored.Title != "renamed" {
		t.Fatalf("expected title=renamed, got %q", stored.Title)
	}
}
