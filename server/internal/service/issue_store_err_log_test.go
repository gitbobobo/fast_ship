package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// INT-215：热点读库失败经 loadStoreErr 归类时，服务端必须落一条带 cause
// 的结构化 Warn——客户端主动取消（context_canceled）、借连接等待/预算到点
// （context_deadline_exceeded）与其余存储故障（store_error）能区分，排查
// 时不再靠客户端统一文案倒推。同时断言：客户端文案口径不变、extra 上下文
// 字段（issue_id）随行携带。

// assertStoreErrLogged 断言观察核里恰好捕获一条「热点读库失败」日志，
// 且 op/cause 字段与级别都等于期望值，返回该条目供进一步断言 extra 字段。
func assertStoreErrLogged(t *testing.T, observed *observer.ObservedLogs, wantOp, wantCause string, wantLevel zapcore.Level) observer.LoggedEntry {
	t.Helper()
	entries := observed.TakeAll()
	if len(entries) != 1 {
		t.Fatalf("应恰好落一条热点读库失败日志，实际 %d 条", len(entries))
	}
	e := entries[0]
	if e.Message != "热点读库失败" {
		t.Fatalf("日志消息应为「热点读库失败」，实际: %s", e.Message)
	}
	if e.Level != wantLevel {
		t.Fatalf("级别应为 %v，实际: %v", wantLevel, e.Level)
	}
	hasOp, hasCause := false, false
	for _, f := range e.Context {
		if f.Key == "op" && f.String == wantOp {
			hasOp = true
		}
		if f.Key == "cause" && f.String == wantCause {
			hasCause = true
		}
	}
	if !hasOp || !hasCause {
		t.Fatalf("日志应携带 op=%s cause=%s，实际字段: %v", wantOp, wantCause, e.Context)
	}
	return e
}

func TestLoadStoreErrLogsCause(t *testing.T) {
	svc := setupTestServices(t)

	// 客户端主动取消：cause=context_canceled，文案口径不变。
	core, observed := observer.New(zapcore.InfoLevel)
	svc.issueService.logger = zap.New(core)
	err := svc.issueService.loadStoreErr("op_under_test", fmt.Errorf("query: %w", context.Canceled))
	assertReadTimeoutErr(t, err)
	e := assertStoreErrLogged(t, observed, "op_under_test", "context_canceled", zapcore.InfoLevel)
	if !hasField(e, "cause", "context_canceled") {
		t.Fatalf("unreachable")
	}

	// 借连接等待/预算到点：cause=context_deadline_exceeded，文案相同但
	// cause 可区分。
	core, observed = observer.New(zapcore.InfoLevel)
	svc.issueService.logger = zap.New(core)
	err = svc.issueService.loadStoreErr("op_under_test", fmt.Errorf("query: %w", context.DeadlineExceeded))
	assertReadTimeoutErr(t, err)
	assertStoreErrLogged(t, observed, "op_under_test", "context_deadline_exceeded", zapcore.WarnLevel)

	// 其余存储故障：cause=store_error，归类 ErrInternal 原样。
	core, observed = observer.New(zapcore.InfoLevel)
	svc.issueService.logger = zap.New(core)
	err = svc.issueService.loadStoreErr("op_under_test", errors.New("disk on fire"))
	if !errors.Is(err, errs.ErrInternal) {
		t.Fatalf("普通错误应归类为 errs.ErrInternal，实际: %v", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("普通错误不应被当作 ctx 错误: %v", err)
	}
	e = assertStoreErrLogged(t, observed, "op_under_test", "store_error", zapcore.WarnLevel)
	if !hasField(e, "error", "disk on fire") {
		t.Fatalf("日志应携带原始错误，实际字段: %v", e.Context)
	}
}

// INT-215：调用方上下文（issue_id 等 extra 字段）必须随统一日志落盘，
// 合并调用点原有手工日志后仍能定位到具体 Issue。
func TestLoadStoreErrCarriesExtraFields(t *testing.T) {
	svc := setupTestServices(t)

	core, observed := observer.New(zapcore.InfoLevel)
	svc.issueService.logger = zap.New(core)

	err := svc.issueService.loadStoreErr("op_under_test", context.Canceled,
		zap.String("issue_id", "issue-1"))
	assertReadTimeoutErr(t, err)
	e := assertStoreErrLogged(t, observed, "op_under_test", "context_canceled", zapcore.InfoLevel)
	if !hasField(e, "issue_id", "issue-1") {
		t.Fatalf("日志应携带 issue_id=issue-1，实际字段: %v", e.Context)
	}
}

// INT-215 审查 R1 Note：请求取消时 sync 走到的是 loadIssueAndProject 的
// issues 查询（loadIssueAndProject 原手工 Error 已合并），断言该行恰好落
// 一条统一日志且 op=load_issue、issue_id 随行。
func TestSyncCanceledContextLogsLoadIssueOnce(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	core, observed := observer.New(zapcore.InfoLevel)
	svc.issueService.logger = zap.New(core)

	_, err := svc.issueService.SyncIssuePullRequests(canceledCtx(t), issue.ID, user.ID)
	assertReadTimeoutErr(t, err)

	e := assertStoreErrLogged(t, observed, "load_issue", "context_canceled", zapcore.InfoLevel)
	if !hasField(e, "issue_id", issue.ID) {
		t.Fatalf("日志应携带 issue_id=%s，实际字段: %v", issue.ID, e.Context)
	}
}

// INT-215 审查 R1 Note：前置读库成功、只有 sync 的 PR 关联列表读库失败时
// （issue_pull_request.go 的 ListByIssueID 分支，原手工 Error 已合并），
// 恰好落一条统一日志：op=sync_pr_links、cause=store_error、Warn、issue_id
// 随行。用 drop 表让该次读库确定性失败，前置读库不碰该表。
func TestSyncPrLinksListFailureLogsOnce(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	if err := svc.db.Migrator().DropTable(&model.IssuePullRequest{}); err != nil {
		t.Fatalf("drop issue_pull_requests table: %v", err)
	}

	core, observed := observer.New(zapcore.InfoLevel)
	svc.issueService.logger = zap.New(core)

	_, err := svc.issueService.SyncIssuePullRequests(context.Background(), issue.ID, user.ID)
	if !errors.Is(err, errs.ErrInternal) {
		t.Fatalf("PR 关联列表读库失败应归类为 errs.ErrInternal，实际: %v", err)
	}

	e := assertStoreErrLogged(t, observed, "sync_pr_links", "store_error", zapcore.WarnLevel)
	if !hasField(e, "issue_id", issue.ID) {
		t.Fatalf("日志应携带 issue_id=%s，实际字段: %v", issue.ID, e.Context)
	}
}

func hasField(e observer.LoggedEntry, key, want string) bool {
	for _, f := range e.Context {
		// zap.Error 的值存 Interface，字符串类字段存 String。
		if f.Key == key && (f.String == want || fmt.Sprint(f.Interface) == want) {
			return true
		}
	}
	return false
}
