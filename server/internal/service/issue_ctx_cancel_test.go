package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// INT-187 热点读库的取消语义：预取消的请求 ctx 必须让借连接等待当场失败，
// 文案统一归为「读取超时，请稍后重试」，且错误链经 errs.Wrap 保留——调用方
// 与测试仍能 errors.Is 到底层 ctx 错误。全部用预取消 ctx，确定性、无 sleep。

// canceledCtx 返回一个已取消的请求 ctx（模拟客户端断开）。
func canceledCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// assertReadTimeoutErr 断言错误是热点读库超时归类：文案「读取超时，请稍后
// 重试」且错误链透出底层 ctx 错误（Canceled 或 DeadlineExceeded，证明
// errs.Wrap 生效）。
func assertReadTimeoutErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望返回错误，实际为 nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误链应透出底层 ctx 错误，实际: %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("错误应为 *errs.AppError，实际: %v (%T)", err, err)
	}
	if appErr.Message != "读取超时，请稍后重试" {
		t.Fatalf("文案应为「读取超时，请稍后重试」，实际: %s", appErr.Message)
	}
}

func TestListCanceledContextReadsTimeout(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	_, _, err := svc.issueService.List(canceledCtx(t), project.ID, user.ID, IssueListFilters{}, 1, 20)
	assertReadTimeoutErr(t, err)
}

func TestCountIssuesCanceledContextReadsTimeout(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	_, err := svc.issueService.CountIssues(canceledCtx(t), project.ID, user.ID, IssueListFilters{})
	assertReadTimeoutErr(t, err)
}

func TestBatchCloseDoneIssuesCanceledContextPreReadsTimeout(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	// 预算只覆盖关闭阶段，前置读库随请求 ctx 取消（INT-187）：预取消 ctx
	// 应在项目查找处当场失败，而不是被 30s 预算兜住。
	_, err := svc.issueService.BatchCloseDoneIssues(canceledCtx(t), project.ID, user.ID, "")
	assertReadTimeoutErr(t, err)
}

func TestSyncIssuePullRequestsCanceledContextResolveTimeout(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	// resolveCtx 派生自请求 ctx（INT-187 阶段 1 预算）：请求已取消时，
	// loadIssueAndProject 的读库当场失败并归类为读取超时。
	_, err := svc.issueService.SyncIssuePullRequests(canceledCtx(t), issue.ID, user.ID)
	assertReadTimeoutErr(t, err)
}

// INT-187 审查 R1：标签读库失败不得被 buildLabelMap 吞掉后经 resolveLabels
// 兑底重查——空页上后续聚合不碰库，吞错会让取消/读库故障折成 200 空列表。
// 项目归属与分页读库正常、标签表损坏时，List 必须把错误交给 loadStoreErr
// 归类后返回错误。
func TestListLabelReadFailureReturnsError(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	if err := svc.db.Migrator().DropTable(&model.GitHubRepoLabel{}); err != nil {
		t.Fatalf("drop label table: %v", err)
	}
	_, _, err := svc.issueService.List(context.Background(), project.ID, user.ID, IssueListFilters{}, 1, 20)
	if !errors.Is(err, errs.ErrInternal) {
		t.Fatalf("标签读取失败应归类为 errs.ErrInternal，实际: %v", err)
	}
}

// INT-187 审查 R1：标签读库走 ctx 视图——取消经 loadStoreErr 归类为读取
// 超时，错误链透出，不被吞掉。
func TestBuildLabelMapCanceledContextReadsTimeout(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	_, err := svc.issueService.withRepos(canceledCtx(t)).buildLabelMap(project.ID)
	assertReadTimeoutErr(t, loadStoreErr(err))
}

func TestLoadStoreErrClassification(t *testing.T) {
	// ctx 错误 → Wrap 文案 + 错误链透出。
	assertReadTimeoutErr(t, loadStoreErr(context.Canceled))
	assertReadTimeoutErr(t, loadStoreErr(context.DeadlineExceeded))
	// 包装后的 ctx 错误（如 repo/driver 包了一层）同样按链归类。
	assertReadTimeoutErr(t, loadStoreErr(fmt.Errorf("query: %w", context.Canceled)))

	// 其余错误 → ErrInternal 原样（无超时文案，也不保留无关错误链）。
	err := loadStoreErr(fmt.Errorf("boom"))
	if !errors.Is(err, errs.ErrInternal) {
		t.Fatalf("普通错误应归类为 errs.ErrInternal，实际: %v", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("普通错误不应被当作 ctx 错误: %v", err)
	}
}
