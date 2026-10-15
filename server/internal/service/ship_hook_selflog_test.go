package service

import (
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// INT-94 复审（Codex P1）：ship-hook 持久化失败的错误日志必须带 user_id，
// 避免以空归属行入库后经 /api/self-logs 泄露失败元数据给其他用户。
func TestExecutePendingShipHooksFailureLogCarriesUserID(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "hook-selflog-user")
	project := createTestProject(t, svc.db, user.ID)
	current := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.VersionNumber = "v2.0.0"
		v.ShipHooksStatus = "pending"
	})
	currentIssue := createTestIssue(t, svc.db, project.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 2
	})
	now := time.Now().UTC()
	if err := svc.shipHookRepo.Upsert(&model.IssueShipHook{
		IssueID: currentIssue.ID, ProjectID: project.ID, Status: model.IssueShipHookStatusPending,
		CommentEnabled: true, CommentBody: "released {version}", CreatedByUserID: user.ID,
		UpdatedByUserID: user.ID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed pending hook: %v", err)
	}

	core, observed := observer.New(zapcore.InfoLevel)
	svc.shipService.logger = zap.New(core)

	// 评论发出后使租约过期，令 CompleteExecution 的比较置位失败，走进降级日志分支。
	actions := &fakeShipHookActions{expireIssueID: currentIssue.ID}
	actions.onComment = func(issueID string) {
		if err := svc.db.Model(&model.IssueShipHook{}).Where("issue_id = ?", issueID).
			Update("lease_expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
			t.Fatalf("expire hook lease: %v", err)
		}
	}
	svc.shipService.hookActions = actions

	if _, err := svc.shipService.ExecutePendingShipHooks(project.ID, user.ID, current); err == nil {
		t.Fatal("expected hook persistence error")
	}

	found := false
	for _, e := range observed.TakeAll() {
		if !strings.Contains(e.Message, "persist ship hook execution result failed") {
			continue
		}
		found = true
		attributed := false
		for _, f := range e.Context {
			if f.Key == "user_id" && f.String == user.ID {
				attributed = true
			}
		}
		if !attributed {
			t.Fatalf("失败日志未携带 user_id=%s: %v", user.ID, e.Context)
		}
	}
	if !found {
		t.Fatalf("未捕获 ship-hook 持久化失败日志")
	}
}
