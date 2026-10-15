package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 自动化（INT-90）service 层定向测试：事件回滚不触发、忙碌重入、令牌消费、
// 结束/超时竞态、通知去重、cron 停机合并触发。调度器测试只驱动 DB 部分
// （dispatchDueCrons / markTimeouts），不触网。

type automationTestEnv struct {
	db     *gorm.DB
	svc    *AutomationService
	sched  *AutomationScheduler
	agent  *AutomationAgentService
	runs   *repository.AutomationRunRepository
	tokens *repository.AutomationRunTokenRepository
}

func setupAutomationTestEnv(t *testing.T) *automationTestEnv {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.T3Environment{},
		&model.Automation{}, &model.AutomationRun{}, &model.AutomationRunToken{}, &model.Notification{},
		&model.Issue{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_automation_trigger ON automation_runs(automation_id, trigger_key)")
	db.Exec("DROP INDEX IF EXISTS idx_automation_runs_active_once")
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_active_once
ON automation_runs(automation_id)
WHERE trigger_type IN ('cron','issue_created','issue_status_changed','issue_label_added')
  AND (finished_at IS NULL OR post_state = 'pending')`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automation_runs_pr_head ON automation_runs(repo_full_name, pr_number, head_sha)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_user_dedup ON notifications(user_id, dedup_key)")

	cfg := &config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}
	logger := zap.NewNop()
	autos := repository.NewAutomationRepository(db)
	runs := repository.NewAutomationRunRepository(db)
	tokens := repository.NewAutomationRunTokenRepository(db)
	sink := NewNotificationSink(db)
	svc := NewAutomationService(db, autos, runs, repository.NewT3EnvironmentRepository(db),
		repository.NewProjectRepository(db), repository.NewIssueRepository(db), sink, cfg, logger)
	sched := NewAutomationScheduler(db, autos, runs, tokens, nil, sink, repository.NewIssueRepository(db), cfg, logger)
	agent := NewAutomationAgentService(db, runs, tokens, nil, sink, logger)
	return &automationTestEnv{db: db, svc: svc, sched: sched, agent: agent, runs: runs, tokens: tokens}
}

func (e *automationTestEnv) createUser(t *testing.T) *model.User {
	t.Helper()
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString() + "@t", PasswordHash: "x"}
	if err := e.db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func (e *automationTestEnv) createAutomation(t *testing.T, userID string, mutate func(*model.Automation)) *model.Automation {
	t.Helper()
	now := time.Now().UTC()
	env := &model.T3Environment{
		ID:                   uuid.NewString(),
		UserID:               userID,
		Name:                 "env-" + uuid.NewString()[:8],
		Address:              "https://t3.example.com",
		BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt:      now.Add(time.Hour),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := e.db.Create(env).Error; err != nil {
		t.Fatalf("create env: %v", err)
	}
	a := &model.Automation{
		ID:             uuid.NewString(),
		UserID:         userID,
		Name:           "夜间回报",
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
		EnvironmentID:  env.ID,
		T3ProjectID:    "t3-proj",
		TriggerType:    model.AutomationTriggerIssueCreated,
		PromptTemplate: "处理 {{issue_title}}",
		RuntimeMode:    "full-access",
		TimeoutSeconds: 3600,
		PostAction:     model.AutomationPostNone,
	}
	if mutate != nil {
		mutate(a)
	}
	if err := e.db.Create(a).Error; err != nil {
		t.Fatalf("create automation: %v", err)
	}
	return a
}

// seedRun 直接落一条活动运行（绕过调度器），供忙碌/超时/竞态用例布置现场。
func (e *automationTestEnv) seedRun(t *testing.T, a *model.Automation, mutate func(*model.AutomationRun)) *model.AutomationRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             a.UserID,
		AutomationID:       a.ID,
		TriggerType:        a.TriggerType,
		TriggerKey:         "event:" + uuid.NewString(),
		TriggeredAt:        now,
		ConfigSnapshotJSON: snapshotJSON(a),
		EnvironmentID:      a.EnvironmentID,
		T3ProjectID:        a.T3ProjectID,
		ThreadID:           uuid.NewString(),
		LaunchCommandID:    uuid.NewString(),
		InitialMessageID:   uuid.NewString(),
		Status:             model.RunStatusPendingRun,
		DeadlineAt:         now.Add(time.Duration(a.TimeoutSeconds) * time.Second),
		PostState:          model.AutomationPostStateNone,
	}
	if mutate != nil {
		mutate(run)
	}
	if err := e.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run
}

func eventFor(a *model.Automation, projectID, issueID string) IssueEvent {
	return IssueEvent{
		Type:       model.AutomationTriggerIssueCreated,
		UserID:     a.UserID,
		ProjectID:  projectID,
		IssueID:    issueID,
		TriggerKey: "event:" + uuid.NewString(),
		OccurredAt: time.Now().UTC(),
	}
}

func countRuns(t *testing.T, db *gorm.DB, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AutomationRun{}).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

func TestEnqueueIssueEventTx_CreatesPendingRun(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)

	err := env.db.Transaction(func(tx *gorm.DB) error {
		return env.svc.EnqueueIssueEventTx(tx, eventFor(a, "p1", "i1"))
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var run model.AutomationRun
	if err := env.db.Where("automation_id = ?", a.ID).First(&run).Error; err != nil {
		t.Fatalf("run not created: %v", err)
	}
	if run.Status != model.RunStatusPendingRun {
		t.Fatalf("run status = %q, want empty (pending claim)", run.Status)
	}
	if run.ThreadID == "" || run.LaunchCommandID == "" || run.InitialMessageID == "" {
		t.Fatal("T3 identities must be preallocated")
	}
	if !run.DeadlineAt.After(run.TriggeredAt) {
		t.Fatal("deadline must be triggered_at + timeout")
	}
}

func TestEnqueueIssueEventTx_RollbackDoesNotTrigger(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)

	// 调用方事务回滚：事件与运行一并消失。
	_ = env.db.Transaction(func(tx *gorm.DB) error {
		if err := env.svc.EnqueueIssueEventTx(tx, eventFor(a, "p1", "i1")); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if n := countRuns(t, env.db, "automation_id = ?", a.ID); n != 0 {
		t.Fatalf("rolled-back event produced %d runs, want 0", n)
	}
}

func TestEnqueueIssueEventTx_FilterAndDisabled(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	project := &model.Project{ID: uuid.NewString(), UserID: user.ID, Name: "proj-2"}
	if err := env.db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	disabled := env.createAutomation(t, user.ID, func(a *model.Automation) { a.Enabled = false })
	otherProject := env.createAutomation(t, user.ID, func(a *model.Automation) {
		a.IssueProjectID = &project.ID
	})

	if err := env.db.Transaction(func(tx *gorm.DB) error {
		if err := env.svc.EnqueueIssueEventTx(tx, eventFor(disabled, "p1", "i1")); err != nil {
			return err
		}
		return env.svc.EnqueueIssueEventTx(tx, eventFor(otherProject, "p1", "i1"))
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if n := countRuns(t, env.db, "automation_id IN ?", []string{disabled.ID, otherProject.ID}); n != 0 {
		t.Fatalf("disabled/project-scoped automations matched %d runs, want 0", n)
	}
}

func TestEnqueueIssueEventTx_BusyRecordsFailedRun(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)

	// 已有活动运行（finished_at IS NULL）。
	env.seedRun(t, a, nil)

	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return env.svc.EnqueueIssueEventTx(tx, eventFor(a, "p1", "i1"))
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var busy model.AutomationRun
	if err := env.db.Where("automation_id = ? AND error_code = ?", a.ID, "automation_busy").First(&busy).Error; err != nil {
		t.Fatalf("busy run not recorded: %v", err)
	}
	if busy.Status != model.AutomationRunFailed || busy.FinishedAt == nil {
		t.Fatalf("busy run status=%q finished=%v, want failed/finished", busy.Status, busy.FinishedAt)
	}
	// 活动运行仍是唯一活动行；部分唯一索引挡住第二条活动行。
	if n := countRuns(t, env.db, "automation_id = ? AND finished_at IS NULL", a.ID); n != 1 {
		t.Fatalf("active runs = %d, want 1", n)
	}

	var note model.Notification
	if err := env.db.Where("user_id = ? AND kind = ?", user.ID, model.NotificationAutomationBusy).First(&note).Error; err != nil {
		t.Fatalf("busy notification: %v", err)
	}
	if note.Title == "" {
		t.Fatal("busy notification missing title")
	}

	// 同一自动化再次忙碌：同指纹只续 last_seen，不刷第二条。
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return env.svc.EnqueueIssueEventTx(tx, eventFor(a, "p1", "i2"))
	}); err != nil {
		t.Fatalf("second busy enqueue: %v", err)
	}
	var notes int64
	if err := env.db.Model(&model.Notification{}).Where("user_id = ? AND kind = ?", user.ID, model.NotificationAutomationBusy).Count(&notes).Error; err != nil {
		t.Fatalf("count busy notes: %v", err)
	}
	if notes != 1 {
		t.Fatalf("busy notifications = %d, want 1 (deduped by automation id)", notes)
	}
}

func TestScheduler_CronMergeAfterDowntime(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	// 停机错过多个周期：next_run_at 落在过去一小时前。
	stale := time.Now().UTC().Add(-time.Hour)
	a := env.createAutomation(t, user.ID, func(a *model.Automation) {
		a.TriggerType = model.AutomationTriggerCron
		a.CronExpression = "*/10 * * * *"
		a.NextRunAt = &stale
	})

	env.sched.dispatchDueCrons(time.Now().UTC())

	if n := countRuns(t, env.db, "automation_id = ?", a.ID); n != 1 {
		t.Fatalf("runs after catch-up = %d, want 1 (merged)", n)
	}
	var run model.AutomationRun
	if err := env.db.Where("automation_id = ?", a.ID).First(&run).Error; err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.TriggerKey != cronTriggerKey(stale) {
		t.Fatalf("trigger_key = %q, want cron:<原计划UTC> %q", run.TriggerKey, cronTriggerKey(stale))
	}

	// next_run_at 已推进到未来：同一轮再跑不再产生运行。
	var refreshed model.Automation
	if err := env.db.First(&refreshed, "id = ?", a.ID).Error; err != nil {
		t.Fatalf("reload automation: %v", err)
	}
	if refreshed.NextRunAt == nil || !refreshed.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("next_run_at = %v, want future", refreshed.NextRunAt)
	}
	env.sched.dispatchDueCrons(time.Now().UTC())
	if n := countRuns(t, env.db, "automation_id = ?", a.ID); n != 1 {
		t.Fatalf("runs after second tick = %d, want 1", n)
	}
}

func TestScheduler_ClaimIsConditional(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)
	run := env.seedRun(t, a, nil)

	ctx := context.Background()
	n, err := env.runs.UpdateColumnsWhere(ctx, run.ID, "status = ?",
		[]any{string(model.RunStatusPendingRun)},
		map[string]any{"status": string(model.AutomationRunDispatched), "dispatch_started_at": time.Now().UTC()})
	if err != nil || n != 1 {
		t.Fatalf("first claim n=%d err=%v, want 1", n, err)
	}
	// 重复投递/第二个 worker：认领 0 行。
	n, err = env.runs.UpdateColumnsWhere(ctx, run.ID, "status = ?",
		[]any{string(model.RunStatusPendingRun)},
		map[string]any{"status": string(model.AutomationRunDispatched)})
	if err != nil || n != 0 {
		t.Fatalf("second claim n=%d err=%v, want 0 rows", n, err)
	}
}

func newAgentTestToken(t *testing.T, env *automationTestEnv, run *model.AutomationRun, deadline time.Time) string {
	t.Helper()
	raw, err := newAutomationRunToken()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if err := env.db.Create(&model.AutomationRunToken{
		RunID:     run.ID,
		TokenHash: hashRunToken(raw),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: deadline,
	}).Error; err != nil {
		t.Fatalf("save token: %v", err)
	}
	return raw
}

func TestReport_StartFinishConsumesToken(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, func(a *model.Automation) {
		a.PostAction = model.AutomationPostSettle
	})
	run := env.seedRun(t, a, nil)
	token := newAgentTestToken(t, env, run, run.DeadlineAt.Add(automationRunTokenGrace))
	identity := &RunTokenIdentity{UserID: user.ID, RunID: run.ID}

	// started：dispatched → running，幂等。
	res, err := env.agent.Report(context.Background(), identity, &AutomationRunReportRequest{Phase: "started"})
	if err != nil || res.Status != string(model.AutomationRunRunning) {
		t.Fatalf("started: res=%+v err=%v", res, err)
	}
	if _, err := env.agent.Report(context.Background(), identity, &AutomationRunReportRequest{Phase: "started"}); err != nil {
		t.Fatalf("started replay must be idempotent: %v", err)
	}

	// finished：completed + 令牌消费 + 后置登记。
	finished := &AutomationRunReportRequest{
		Phase:   "finished",
		Summary: api.Ptr("搞定"),
	}
	res, err = env.agent.Report(context.Background(), identity, finished)
	if err != nil || res.Status != string(model.AutomationRunCompleted) {
		t.Fatalf("finished: res=%+v err=%v", res, err)
	}
	var tokenRow model.AutomationRunToken
	if err := env.db.Where("run_id = ?", run.ID).First(&tokenRow).Error; err != nil {
		t.Fatalf("token: %v", err)
	}
	if tokenRow.ConsumedAt == nil {
		t.Fatal("token must be consumed by finish report")
	}
	var stored model.AutomationRun
	if err := env.db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if stored.PostState != model.AutomationPostStatePending || stored.PostDeadlineAt == nil {
		t.Fatalf("post not registered: state=%s deadline=%v", stored.PostState, stored.PostDeadlineAt)
	}

	// 读取端点立即失效（requireLive）。
	if _, err := env.agent.ResolveRunToken(context.Background(), token, true); !errors.Is(err, errs.ErrRunTokenInvalid) {
		t.Fatalf("live resolve after consume = %v, want 40106", err)
	}
	// 回报端点仅允许完全相同重放。
	replay, err := env.agent.Report(context.Background(), identity, finished)
	if err != nil || !replay.Replay {
		t.Fatalf("identical replay: res=%+v err=%v", replay, err)
	}
	different := &AutomationRunReportRequest{Phase: "finished", Summary: api.Ptr("另一个结果")}
	if _, err := env.agent.Report(context.Background(), identity, different); !errors.Is(err, errs.ErrAutomationRunReportConflict) {
		t.Fatalf("different replay = %v, want 40917", err)
	}
	if _, err := env.agent.Report(context.Background(), identity, &AutomationRunReportRequest{Phase: "started"}); !errors.Is(err, errs.ErrAutomationRunReportConflict) {
		t.Fatalf("started after finish = %v, want 40917", err)
	}
}

func TestReport_FinishBeforeStart(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)
	run := env.seedRun(t, a, func(r *model.AutomationRun) { r.Status = model.AutomationRunDispatched })
	newAgentTestToken(t, env, run, run.DeadlineAt.Add(automationRunTokenGrace))

	res, err := env.agent.Report(context.Background(),
		&RunTokenIdentity{UserID: user.ID, RunID: run.ID},
		&AutomationRunReportRequest{Phase: "finished", Summary: apiPtr("直接结束")})
	if err != nil || res.Status != string(model.AutomationRunCompleted) {
		t.Fatalf("finish before start: res=%+v err=%v", res, err)
	}
}

func TestReport_TimeoutAndFinishRace(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)

	t.Run("timeout first wins", func(t *testing.T) {
		a := env.createAutomation(t, user.ID, nil)
		past := time.Now().UTC().Add(-time.Minute)
		run := env.seedRun(t, a, func(r *model.AutomationRun) {
			r.Status = model.AutomationRunRunning
			r.DeadlineAt = past
		})
		newAgentTestToken(t, env, run, past.Add(automationRunTokenGrace))

		env.sched.markTimeouts(time.Now().UTC())
		var stored model.AutomationRun
		if err := env.db.First(&stored, "id = ?", run.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.Status != model.AutomationRunTimedOut {
			t.Fatalf("status = %s, want timed_out", stored.Status)
		}

		// 超时后的首次结束不能改成 completed，只登记摘要。
		res, err := env.agent.Report(context.Background(),
			&RunTokenIdentity{UserID: user.ID, RunID: run.ID},
			&AutomationRunReportRequest{Phase: "finished", Summary: api.Ptr("迟到回报")})
		if err != nil || res.Status != string(model.AutomationRunTimedOut) {
			t.Fatalf("late finish: res=%+v err=%v", res, err)
		}
		if err := env.db.First(&stored, "id = ?", run.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.Summary != "迟到回报" {
			t.Fatalf("summary = %q", stored.Summary)
		}
	})

	t.Run("finish first wins", func(t *testing.T) {
		a := env.createAutomation(t, user.ID, nil)
		past := time.Now().UTC().Add(-time.Minute)
		run := env.seedRun(t, a, func(r *model.AutomationRun) {
			r.Status = model.AutomationRunRunning
			r.DeadlineAt = past
		})
		newAgentTestToken(t, env, run, past.Add(automationRunTokenGrace))

		if _, err := env.agent.Report(context.Background(),
			&RunTokenIdentity{UserID: user.ID, RunID: run.ID},
			&AutomationRunReportRequest{Phase: "finished", Summary: api.Ptr("按时")}); err != nil {
			t.Fatalf("finish: %v", err)
		}
		env.sched.markTimeouts(time.Now().UTC())
		var stored model.AutomationRun
		if err := env.db.First(&stored, "id = ?", run.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.Status != model.AutomationRunCompleted {
			t.Fatalf("status = %s, want completed (finish beat timeout)", stored.Status)
		}
	})
}

func TestNotificationDedup(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	sink := NewNotificationSink(env.db)

	input := NotificationInput{
		UserID:        user.ID,
		Kind:          model.NotificationAutomationRunTimeout,
		EnvironmentID: "env-1",
		ThreadID:      "thread-1",
		RunID:         "run-1",
		ProblemKey:    "run-1",
		StateKey:      "timed_out",
		Title:         "超时",
		Body:          "第一次",
	}
	if err := sink.Publish(input); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := sink.Publish(input); err != nil {
		t.Fatalf("publish again: %v", err)
	}

	var rows []model.Notification
	if err := env.db.Where("user_id = ?", user.ID).Find(&rows).Error; err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("notifications = %d, want 1 (same key same state)", len(rows))
	}
	first := rows[0]
	if err := env.db.Model(&model.Notification{}).Where("id = ?", first.ID).
		Update("read_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("mark read: %v", err)
	}

	// 状态变化：更新内容并清已读。
	input.Body = "第二次（状态变化）"
	input.StateKey = "timed_out:late_report"
	if err := sink.Publish(input); err != nil {
		t.Fatalf("publish changed: %v", err)
	}
	var after model.Notification
	if err := env.db.First(&after, "id = ?", first.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Body != input.Body {
		t.Fatalf("body = %q, want updated", after.Body)
	}
	if after.ReadAt != nil {
		t.Fatal("read_at must be cleared on state change")
	}

	// Agent 通知与系统通知互不影响；kind 校验拒绝未知类型。
	// Agent 通知不能冒充系统 kind：未知类型在 sink 层被拒。
	impersonation := NotificationInput{
		UserID:     user.ID,
		Kind:       model.NotificationKind("system_dispatch_failed"),
		Title:      "冒充",
		ProblemKey: "x",
	}
	if err := sink.Publish(impersonation); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestPublishNotification_RequiresProblemAndStateKey(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)
	run := env.seedRun(t, a, func(r *model.AutomationRun) {
		r.Status = model.AutomationRunRunning
	})
	identity := &RunTokenIdentity{UserID: user.ID, RunID: run.ID}

	if err := env.agent.PublishNotification(context.Background(), identity, &api.AgentNotificationRequest{
		Title: "卡住了",
	}); err == nil {
		t.Fatal("missing problem_key/state_key must fail")
	}

	if err := env.agent.PublishNotification(context.Background(), identity, &api.AgentNotificationRequest{
		Title:      "卡住了",
		ProblemKey: "stuck-wait",
		StateKey:   "waiting-approval",
		Body:       api.Ptr("请在线程里点批准"),
	}); err != nil {
		t.Fatalf("valid agent notification: %v", err)
	}
	var n model.Notification
	if err := env.db.Where("user_id = ? AND kind = ?", user.ID, model.NotificationAgent).First(&n).Error; err != nil {
		t.Fatalf("agent notification: %v", err)
	}
	if n.Title != "卡住了" {
		t.Fatalf("title = %q", n.Title)
	}
}

func TestParseAutomationCron_RejectsNonStandard(t *testing.T) {
	if _, _, err := parseAutomationCron("*/10 * * * * *", "", time.Now()); err == nil {
		t.Fatal("six-field (seconds) expression must be rejected")
	}
	if _, _, err := parseAutomationCron("@every 10s", "", time.Now()); err == nil {
		t.Fatal("@every descriptor must be rejected")
	}
	if _, _, err := parseAutomationCron("CRON_TZ=Asia/Shanghai */10 * * * *", "", time.Now()); err == nil {
		t.Fatal("inline timezone prefix must be rejected")
	}
	if _, _, err := parseAutomationCron("*/10 * * * *", "Not/AZone", time.Now()); err == nil {
		t.Fatal("invalid IANA timezone must be rejected")
	}
	if _, next, err := parseAutomationCron("*/10 * * * *", "Asia/Shanghai", time.Now()); err != nil || !next.After(time.Now()) {
		t.Fatalf("valid expression: next=%v err=%v", next, err)
	}
}
