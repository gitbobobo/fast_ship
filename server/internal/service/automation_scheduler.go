package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AutomationScheduler 自动化协调循环（INT-90）：单进程内一个每 10 秒的协调者
// 加固定 4 个派发 worker。协调者每轮做四件事：
//
//  1. 到期 cron 触发器 → 短事务乐观核对（父行 enabled + 触发器旧 next_run_at）
//     并写运行行（trigger_id/trigger_type 落触发器）；停机错过的触发合并为一次，
//     trigger_key=cron:<原计划UTC>；
//  2. 全量扫描待认领运行（status=''，含 Issue 事件在调用方事务里写的行），
//     投递给 worker；worker 用条件 UPDATE 认领，天然幂等；
//  3. 超时：活动运行过 deadline_at → timed_out，按 post_on_timeout 登记后置；
//  4. 待执行后置：等线程安静（最多 post_deadline 窗口）后下发 settle/archive。
//
// 事务纪律：MaxOpenConns=1，事务内绝不调 T3、绝不复用根 DB 再开查询；网络
// 调用全部在事务外。

const (
	automationTickInterval      = 10 * time.Second
	automationDispatchWorkers   = 4
	automationDispatchQueue     = 128
	automationBatchLimit        = 50
	automationDispatchBudget    = 60 * time.Second
	automationPostCommandBudget = 30 * time.Second
)

type AutomationScheduler struct {
	db        *gorm.DB
	autos     *repository.AutomationRepository
	runs      *repository.AutomationRunRepository
	tokens    *repository.AutomationRunTokenRepository
	threads   *T3ThreadsService
	sink      *NotificationSink
	issueRepo *repository.IssueRepository
	cfg       *config.Config
	logger    *zap.Logger

	// prReview GitHub PR 评审的协调面（INT-93）；可 nil（测试/未启用）。
	prReview *PRReviewService

	dispatchQueue chan string
	// kickCh 缓冲 1 的立即触发：webhook 提交防抖/开轮后踢协调循环补一轮 tick
	//（循环仍是唯一执行者，kick 只是把下一次 tick 提前）。
	kickCh   chan struct{}
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func NewAutomationScheduler(
	db *gorm.DB,
	autos *repository.AutomationRepository,
	runs *repository.AutomationRunRepository,
	tokens *repository.AutomationRunTokenRepository,
	threads *T3ThreadsService,
	sink *NotificationSink,
	issueRepo *repository.IssueRepository,
	cfg *config.Config,
	logger *zap.Logger,
) *AutomationScheduler {
	return &AutomationScheduler{
		db: db, autos: autos, runs: runs, tokens: tokens,
		threads: threads, sink: sink, issueRepo: issueRepo,
		cfg: cfg, logger: logger,
		dispatchQueue: make(chan string, automationDispatchQueue),
		kickCh:        make(chan struct{}, 1),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
}

// UsePRReview 挂接 GitHub PR 评审协调面（INT-93）；调度循环负责驱动它的两个
// 事务外步骤（到期防抖、评论/check 同步）。
func (s *AutomationScheduler) UsePRReview(pr *PRReviewService) {
	s.prReview = pr
	pr.SetKick(s.Kick)
}

// Kick 非阻塞请求立即补一轮 tick（缓冲 1，已挂起时合并）。
func (s *AutomationScheduler) Kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

// Start 启动协调循环与派发 worker；Stop 之前一直运行。
func (s *AutomationScheduler) Start() {
	for i := 0; i < automationDispatchWorkers; i++ {
		go s.dispatchWorker()
	}
	go s.coordinateLoop()
}

// Stop 停止调度（不等待进行中的 T3 网络调用完成——进程退出时由调用方兜底）。
func (s *AutomationScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	<-s.doneCh
}

func (s *AutomationScheduler) coordinateLoop() {
	defer close(s.doneCh)
	ticker := time.NewTicker(automationTickInterval)
	defer ticker.Stop()
	s.tick(time.Now().UTC())
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.kickCh:
			s.tick(time.Now().UTC())
		case <-ticker.C:
			s.tick(time.Now().UTC())
		}
	}
}

func (s *AutomationScheduler) tick(now time.Time) {
	// 每步独立容错：一步失败不拖垮同轮其它步骤。
	s.dispatchDueCrons(now)
	if s.prReview != nil {
		// PR 评审：到期防抖开轮（在待认领扫描之前，让本轮立即派发新提升的运行）。
		s.prReview.ProcessDueDebounces(now)
	}
	s.queueClaimableRuns(now)
	s.markTimeouts(now)
	if s.prReview != nil {
		// 超时释放名额后提升排队；再扫一遍待认领，避免再等 10 秒。
		s.prReview.PromoteQueuedPRRuns(now)
		s.queueClaimableRuns(now)
	}
	s.runDuePostActions(now)
	if s.prReview != nil {
		// PR 评审：评论/check 同步（纯 GitHub 调用，放最后不阻塞派发）。
		s.prReview.SyncPendingReviews(now)
	}
}

// dispatchDueCrons 步骤 1：到期 cron 触发器（INT-118 起调度单位是触发器）。
// 短事务内核对父 automation enabled + 触发器旧 next_run_at 未变才写运行并推进
// 调度；两台进程（理论上单进程部署）或协调者与用户更新并发时靠这个乐观条件收敛。
func (s *AutomationScheduler) dispatchDueCrons(now time.Time) {
	due, err := repository.ListDueCronTriggersTx(s.db, now, automationBatchLimit)
	if err != nil {
		s.logger.Error("查询到期 cron 触发器失败", zap.Error(err))
		return
	}
	for i := range due {
		s.dispatchDueCronTrigger(&due[i], now)
	}
}

func (s *AutomationScheduler) dispatchDueCronTrigger(t *model.AutomationTrigger, now time.Time) {
	planned := *t.NextRunAt
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var a model.Automation
		if err := tx.Where("id = ? AND enabled = ?", t.AutomationID, true).First(&a).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 已被停用/删除，跳过
			}
			return err
		}
		var current model.AutomationTrigger
		if err := tx.Where("id = ?", t.ID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil // 触发器已被删除，跳过
			}
			return err
		}
		if current.NextRunAt == nil || !current.NextRunAt.Equal(planned) {
			return nil // next_run_at 已被并发推进或重排，跳过
		}

		event := IssueEvent{
			ID:         uuid.NewString(),
			Type:       model.AutomationTriggerCron,
			UserID:     a.UserID,
			TriggerKey: cronTriggerKey(planned),
			OccurredAt: now,
		}
		contextJSON, err := marshalJSON(AutomationTriggerContext{})
		if err != nil {
			return err
		}

		// 忙碌判定按触发器：同一自动化的其它触发器可并行运行。
		busy, err := repository.HasActiveRunByTriggerTx(tx, t.ID)
		if err != nil {
			return err
		}
		run := newAutomationRunRow(&a, &current, event, contextJSON, now)
		if busy {
			finished := now
			run.Status = model.AutomationRunFailed
			run.FinishedAt = &finished
			run.ErrorCode = "automation_busy"
			run.ErrorMessage = "自动化已有进行中的运行（或后置操作待执行），本次触发未执行"
		}
		created := true
		if err := tx.Create(run).Error; err != nil {
			if !isRunDuplicateTriggerKey(err) {
				return err
			}
			created = false
		}
		if busy && created {
			if err := publishAutomationBusyTx(s.sink, tx, &a, run); err != nil {
				return err
			}
		}

		// 推进触发器 next_run_at：以 now 为基线取下一次，停机期间错过的全部
		// 合并为这一次触发（trigger_key 记录原计划时间，重放撞唯一约束被忽略）。
		_, next, err := parseAutomationCron(current.CronExpression, current.Timezone, now)
		if err != nil {
			// 配置损坏（如表达式被手工改库）：停该触发器调度并告警，不无限循环。
			s.logger.With(zap.String("user_id", a.UserID)).Error("cron 表达式解析失败，暂停该触发器的后续调度",
				zap.String("automation_id", t.AutomationID), zap.String("trigger_id", t.ID),
				zap.String("user_id", a.UserID), zap.Error(err))
			return tx.Model(&model.AutomationTrigger{}).Where("id = ?", t.ID).
				Update("next_run_at", nil).Error
		}
		res := tx.Model(&model.AutomationTrigger{}).
			Where("id = ? AND next_run_at = ?", t.ID, planned).
			Update("next_run_at", next)
		if res.Error != nil {
			return res.Error
		}
		return nil
	})
	if err != nil {
		s.logger.Error("处理到期 cron 触发器失败",
			zap.String("automation_id", t.AutomationID), zap.String("trigger_id", t.ID), zap.Error(err))
	}
}

// cronTriggerKey 停机错过的 cron 合并触发一次；key 记录原计划时间（UTC）。
func cronTriggerKey(planned time.Time) string {
	return "cron:" + planned.UTC().Format(time.RFC3339Nano)
}

// queueClaimableRuns 步骤 2：把所有待认领运行投给 worker。认领是条件 UPDATE，
// 同一运行即使被重复投递也只会被派发一次。
func (s *AutomationScheduler) queueClaimableRuns(now time.Time) {
	items, err := s.runs.ListClaimableTx(s.db, automationBatchLimit)
	if err != nil {
		s.logger.Error("查询待派发运行失败", zap.Error(err))
		return
	}
	for i := range items {
		select {
		case s.dispatchQueue <- items[i].ID:
		default:
			s.logger.Warn("派发队列已满，本轮跳过部分运行", zap.Int("pending", len(items)-i))
			return
		}
	}
}

// markTimeouts 步骤 3：过 deadline 的活动运行置 timed_out。结束回报与超时是
// 条件更新竞速：先到者定终态（超时后首次结束不能改成 completed）。
func (s *AutomationScheduler) markTimeouts(now time.Time) {
	active, err := s.runs.ListActivePastDeadlineTx(s.db, now, automationBatchLimit)
	if err != nil {
		s.logger.Error("查询超时运行失败", zap.Error(err))
		return
	}
	for i := range active {
		run := active[i]
		s.timeoutRun(&run, now)
	}
}

func (s *AutomationScheduler) timeoutRun(run *model.AutomationRun, now time.Time) {
	do := func() {
		s.timeoutRunTx(run, now)
	}
	// PR 运行与 agent SubmitReview 共用 per-PR 锁，避免超时提交之后仍发出
	// REQUEST_CHANGES。先锁再开事务，顺序与 webhook/agent 一致。
	if run.TriggerType.IsPRTrigger() && s.prReview != nil && run.RepoFullName != "" && run.PRNumber != 0 {
		s.prReview.withPRLock(run.RepoFullName, run.PRNumber, do)
		return
	}
	do()
}

func (s *AutomationScheduler) timeoutRunTx(run *model.AutomationRun, now time.Time) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		cols := map[string]any{
			"status":        string(model.AutomationRunTimedOut),
			"finished_at":   run.DeadlineAt,
			"error_code":    "run_timeout",
			"error_message": "运行超过回报截止时间仍未收到结束回报",
		}
		n, err := s.runs.UpdateColumnsWhereTx(tx, run.ID,
			"status IN (?, ?)",
			[]any{string(model.AutomationRunDispatched), string(model.AutomationRunRunning)}, cols)
		if err != nil || n == 0 {
			return err // 0 行 = 结束回报抢先落了终态
		}

		// 超时后置：仅 post_on_timeout 且线程已确认建成。
		snap := parseAutomationSnapshot(run.ConfigSnapshotJSON)
		if snap.PostAction != model.AutomationPostNone && snap.PostOnTimeout && run.DispatchedAt != nil {
			if err := registerPostPending(tx, run.ID, run.DeadlineAt); err != nil {
				return err
			}
		}

		// PR 评审（INT-93）：状态变化置同步标记（事务外推评论/check）。
		if err := markPRReviewPendingTx(tx, run, now); err != nil {
			return err
		}

		return s.sink.PublishTx(tx, NotificationInput{
			UserID:        run.UserID,
			Kind:          model.NotificationAutomationRunTimeout,
			EnvironmentID: run.EnvironmentID,
			ThreadID:      run.ThreadID,
			RunID:         run.ID,
			StateKey:      "timed_out",
			Title:         "自动化运行超时",
			Body: fmt.Sprintf("「%s」的运行（%s）超过 %d 秒未回报结果，已标记为超时。",
				snap.AutomationName, shortRunID(run.ID), int(run.DeadlineAt.Sub(run.TriggeredAt).Seconds())),
		})
	})
	if err != nil {
		s.logger.Error("标记超时失败", zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
	}
}

// registerPostPending 在事务内为运行登记待执行后置（post 窗口从现在起算）。
func registerPostPending(tx *gorm.DB, runID string, now time.Time) error {
	return tx.Model(&model.AutomationRun{}).Where("id = ?", runID).
		Updates(map[string]any{
			"post_state":           string(model.AutomationPostStatePending),
			"post_attempt":         0,
			"post_next_attempt_at": now,
			"post_deadline_at":     now.Add(automationPostWindow),
		}).Error
}

// runDuePostActions 步骤 4：执行到期的 settle/archive。T3 调用在事务外；
// 运行行更新用条件 UPDATE（WHERE post_state='pending' AND post_attempt=?），
// 与超时/回报路径并发安全。
func (s *AutomationScheduler) runDuePostActions(now time.Time) {
	due, err := s.runs.ListDuePostTx(s.db, now, automationBatchLimit)
	if err != nil {
		s.logger.Error("查询待后置运行失败", zap.Error(err))
		return
	}
	for i := range due {
		s.handlePostAction(&due[i], now)
	}
}

func (s *AutomationScheduler) handlePostAction(run *model.AutomationRun, now time.Time) {
	snap := parseAutomationSnapshot(run.ConfigSnapshotJSON)
	if snap.PostAction == model.AutomationPostNone {
		s.finishPost(run, model.AutomationPostStateNone, "post action cleared", now)
		return
	}

	// 窗口耗尽：不再尝试，标失败并通知。
	if run.PostDeadlineAt != nil && now.After(*run.PostDeadlineAt) {
		s.failPost(run, "后置操作窗口耗尽", now)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), automationPostCommandBudget)
	defer cancel()

	// settle 前等线程安静（无活动 run、无待输入）；archive 可终止活动线程，
	// 不等待。等待也计入 post 窗口，窗口耗尽按失败处理。
	if snap.PostAction == model.AutomationPostSettle {
		quiet, err := s.threads.ThreadSettleSafe(ctx, run.UserID, run.EnvironmentID, run.ThreadID)
		if err != nil {
			s.logger.With(zap.String("user_id", run.UserID)).Warn("读取线程运行状态失败，稍后重试后置",
				zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
			s.retryPost(run, now)
			return
		}
		if !quiet {
			s.retryPost(run, now)
			return
		}
	}

	// commandId 按 attempt 派生：网络失败重放同 commandId（attempt 不变）；
	// 上游明确拒绝才 attempt+1 换新 ID。
	commandID := fmt.Sprintf("fs-auto:%s:post:%s:%d", run.ID, snap.PostAction, run.PostAttempt)
	outcome, err := s.threads.ManageThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID,
		snap.PostAction, commandID)
	switch outcome {
	case t3PostAccepted:
		s.succeedPost(run, now)
	case t3PostRejected:
		s.logger.With(zap.String("user_id", run.UserID)).Warn("后置命令被上游拒绝",
			zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.String("action", string(snap.PostAction)), zap.Error(err))
		s.bumpPostAttempt(run, shortenT3Error(err), now)
	case t3PostTransportError:
		// 未送达或结果未知：同 commandId 原样重试。
		s.retryPost(run, now)
	}
}

// retryPost 安排同 attempt 的下一次重试（网络失败/线程未安静）。
func (s *AutomationScheduler) retryPost(run *model.AutomationRun, now time.Time) {
	next := now.Add(automationPostRetryDelay)
	_, err := s.runs.UpdateColumnsWhere(context.Background(), run.ID,
		"post_state = ? AND post_attempt = ?",
		[]any{string(model.AutomationPostStatePending), run.PostAttempt},
		map[string]any{"post_next_attempt_at": next})
	if err != nil {
		s.logger.Error("更新后置重试时间失败", zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
	}
}

// bumpPostAttempt 上游明确拒绝后换新 commandId 重试（attempt+1）。
func (s *AutomationScheduler) bumpPostAttempt(run *model.AutomationRun, reason string, now time.Time) {
	next := now.Add(automationPostRetryDelay)
	_, err := s.runs.UpdateColumnsWhere(context.Background(), run.ID,
		"post_state = ? AND post_attempt = ?",
		[]any{string(model.AutomationPostStatePending), run.PostAttempt},
		map[string]any{
			"post_attempt":         run.PostAttempt + 1,
			"post_next_attempt_at": next,
			"post_error":           truncate(reason, 500),
		})
	if err != nil {
		s.logger.Error("推进后置尝试失败", zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
	}
}

func (s *AutomationScheduler) succeedPost(run *model.AutomationRun, now time.Time) {
	_, err := s.runs.UpdateColumnsWhere(context.Background(), run.ID,
		"post_state = ?", []any{string(model.AutomationPostStatePending)},
		map[string]any{"post_state": string(model.AutomationPostStateSucceeded), "post_error": ""})
	if err != nil {
		s.logger.Error("标记后置成功失败", zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
	}
}

// failPost 窗口耗尽或状态漂移时的终局：后置失败不影响 agent 回报的结果。
func (s *AutomationScheduler) failPost(run *model.AutomationRun, reason string, now time.Time) {
	snap := parseAutomationSnapshot(run.ConfigSnapshotJSON)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.AutomationRun{}).
			Where("id = ? AND post_state = ?", run.ID, string(model.AutomationPostStatePending)).
			Updates(map[string]any{
				"post_state": string(model.AutomationPostStateFailed),
				"post_error": truncate(reason, 500),
			}).Error; err != nil {
			return err
		}
		return s.sink.PublishTx(tx, NotificationInput{
			UserID:        run.UserID,
			Kind:          model.NotificationAutomationPostFailed,
			EnvironmentID: run.EnvironmentID,
			ThreadID:      run.ThreadID,
			RunID:         run.ID,
			StateKey:      "post_failed:" + reason,
			Title:         "自动化后置操作失败",
			Body: fmt.Sprintf("「%s」运行（%s）的 %s 操作未能完成：%s",
				snap.AutomationName, shortRunID(run.ID), postActionLabel(snap.PostAction), reason),
		})
	})
	if err != nil {
		s.logger.Error("标记后置失败失败", zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
	}
}

// finishPost 后置动作在快照中被清空（防御分支）：静默结束。
func (s *AutomationScheduler) finishPost(run *model.AutomationRun, state model.AutomationPostState, reason string, now time.Time) {
	_, _ = s.runs.UpdateColumnsWhere(context.Background(), run.ID,
		"post_state = ?", []any{string(model.AutomationPostStatePending)},
		map[string]any{"post_state": string(state)})
}

// dispatchWorker 认领并派发运行。认领是条件 UPDATE（status ” → dispatched），
// 多 worker 与重复投递下每个运行只会被派发一次。
func (s *AutomationScheduler) dispatchWorker() {
	for {
		select {
		case <-s.stopCh:
			return
		case runID := <-s.dispatchQueue:
			s.dispatchRun(runID)
		}
	}
}

func (s *AutomationScheduler) dispatchRun(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), automationDispatchBudget)
	defer cancel()

	n, err := s.runs.UpdateColumnsWhere(ctx, runID,
		"status = ?", []any{string(model.RunStatusPendingRun)},
		map[string]any{"status": string(model.AutomationRunDispatched), "dispatch_started_at": time.Now().UTC()})
	if err != nil {
		s.logger.Error("认领运行失败", zap.String("run_id", runID), zap.Error(err))
		return
	}
	if n == 0 {
		return // 已被认领或已被回报/超时终结
	}

	run, err := s.runs.FindByID(ctx, runID)
	if err != nil {
		s.logger.Error("读取运行失败", zap.String("run_id", runID), zap.Error(err))
		return
	}
	s.launchThreadForRun(ctx, run)
}

// launchThreadForRun 签发运行令牌、渲染提示词并经 CreateThread 开线程。
// 事务外执行：整个流程只有短 UPDATE 落库，网络失败不改库状态（保持
// dispatched 等回报或 deadline）。
func (s *AutomationScheduler) launchThreadForRun(ctx context.Context, run *model.AutomationRun) {
	snap := parseAutomationSnapshot(run.ConfigSnapshotJSON)

	publicURL := strings.TrimSpace(s.cfg.Server.PublicURL)
	if publicURL == "" {
		s.failDispatch(run, "dispatch_public_url_missing", "server.public_url 未配置，无法派发", nowUTC())
		return
	}

	token, err := newAutomationRunToken()
	if err != nil {
		s.failDispatch(run, "dispatch_token_error", "生成运行令牌失败", nowUTC())
		return
	}
	now := nowUTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var current model.AutomationRun
		if err := tx.Where("id = ?", run.ID).First(&current).Error; err != nil {
			return err
		}
		// 认领后、签发前可能已被新 SHA 取消：不签发令牌，避免取消后仍能代发。
		if current.Status != model.AutomationRunDispatched && current.Status != model.RunStatusPendingRun {
			return errAutomationLaunchAborted
		}
		return s.tokens.CreateInTx(tx, &model.AutomationRunToken{
			RunID:     run.ID,
			TokenHash: hashRunToken(token),
			CreatedAt: now,
			ExpiresAt: run.DeadlineAt.Add(automationRunTokenGrace),
		})
	})
	if err != nil {
		if errors.Is(err, errAutomationLaunchAborted) {
			return
		}
		s.failDispatch(run, "dispatch_token_error", "保存运行令牌失败", now)
		return
	}

	// 签发后、开线程前再读一次：已取消则不建孤儿线程。
	if fresh, err := s.runs.FindByID(ctx, run.ID); err == nil &&
		fresh.Status != model.AutomationRunDispatched && fresh.Status != model.RunStatusPendingRun {
		return
	}

	prompt := s.buildRunPrompt(run, snap, token, publicURL)
	req := buildT3LaunchRequest(run, snap, prompt)

	result, launchSent, err := s.threads.CreateThreadForAutomation(ctx, run.UserID, run.EnvironmentID, req)
	now = nowUTC()
	switch {
	case err == nil:
		// 线程确认建成：记录 dispatched_at（后置操作只对已确认运行执行）。
		if _, err := s.runs.UpdateColumnsWhere(context.Background(), run.ID,
			"status = ?", []any{model.AutomationRunDispatched},
			map[string]any{"dispatched_at": now}); err != nil {
			s.logger.Error("记录派发时间失败", zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
		}
		// PR 评审（INT-93）先停后建竞态：CreateThread 期间运行可能已被新 SHA
		// 取消——立刻再读状态，已取消就马上中断刚建的线程并消费令牌。
		if run.TriggerType.IsPRTrigger() {
			if fresh, err := s.runs.FindByID(context.Background(), run.ID); err == nil &&
				fresh.Status == model.AutomationRunCancelled && s.prReview != nil {
				s.prReview.OnLaunchCancelled(fresh, now)
			}
		}
		s.logger.With(zap.String("user_id", run.UserID)).Info("自动化线程已启动",
			zap.String("run_id", run.ID),
			zap.String("user_id", run.UserID),
			zap.String("thread_id", result.ThreadId),
			zap.Bool("resumed", result.Resumed))
	case launchSent:
		// RPC 已发出但结果未确认：保留预分配 thread_id 等回报或 deadline，
		// 不自动重试。若期间已被取消，立刻消费令牌并中断（与成功路径同口径）。
		if run.TriggerType.IsPRTrigger() {
			if fresh, err := s.runs.FindByID(context.Background(), run.ID); err == nil &&
				fresh.Status == model.AutomationRunCancelled && s.prReview != nil {
				s.prReview.OnLaunchCancelled(fresh, now)
			}
		}
		s.logger.With(zap.String("user_id", run.UserID)).Warn("线程启动 RPC 已发出但结果未确认，等待回报或超时",
			zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
	default:
		s.failDispatch(run, "dispatch_failed", shortenT3Error(err), now)
	}
}

// errAutomationLaunchAborted 运行在签发令牌前已离开 dispatched/”（取消等）。
var errAutomationLaunchAborted = errors.New("automation: launch aborted")

// failDispatch 派发失败（未送达/拒绝）：终态 + 站内通知。
func (s *AutomationScheduler) failDispatch(run *model.AutomationRun, code, message string, now time.Time) {
	snap := parseAutomationSnapshot(run.ConfigSnapshotJSON)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		n, err := s.runs.UpdateColumnsWhereTx(tx, run.ID,
			"status IN (?, ?)", []any{model.RunStatusPendingRun, model.AutomationRunDispatched},
			map[string]any{
				"status":        string(model.AutomationRunFailed),
				"finished_at":   now,
				"error_code":    code,
				"error_message": truncate(message, 500),
			})
		if err != nil || n == 0 {
			return err
		}
		// PR 评审（INT-93）：失败也要推到评论/check。
		if err := markPRReviewPendingTx(tx, run, now); err != nil {
			return err
		}
		return s.sink.PublishTx(tx, NotificationInput{
			UserID:        run.UserID,
			Kind:          model.NotificationAutomationDispatchFailed,
			EnvironmentID: run.EnvironmentID,
			ThreadID:      run.ThreadID,
			RunID:         run.ID,
			StateKey:      "dispatch_failed:" + code,
			Title:         "自动化运行启动失败",
			Body: fmt.Sprintf("「%s」的运行（%s）启动失败：%s",
				snap.AutomationName, shortRunID(run.ID), message),
		})
	})
	if err != nil {
		s.logger.Error("记录派发失败失败", zap.String("run_id", run.ID), zap.String("user_id", run.UserID), zap.Error(err))
	}
	s.Kick()
}

// buildRunPrompt 渲染提示词：用户模板占位符 + 服务端回报说明（public_url + 令牌）。
// Issue 正文派发时从 Issue 库现读，不落运行记录；PR 评审运行带 PR 上下文与
// 代发端点说明。
func (s *AutomationScheduler) buildRunPrompt(run *model.AutomationRun, snap automationConfigSnapshot, token, publicURL string) string {
	ctxData := map[string]string{
		"{{issue_id}}":    "",
		"{{issue_title}}": "",
		"{{issue_body}}":  "",
		"{{trigger_key}}": run.TriggerKey,
	}
	var triggerCtx AutomationTriggerContext
	if json.Unmarshal([]byte(run.TriggerContextJSON), &triggerCtx) == nil && triggerCtx.IssueId != nil && *triggerCtx.IssueId != "" {
		ctxData["{{issue_id}}"] = *triggerCtx.IssueId
		if issue, err := s.issueRepo.FindByID(*triggerCtx.IssueId); err == nil {
			ctxData["{{issue_title}}"] = issue.Title
			ctxData["{{issue_body}}"] = issue.Body
		} else if triggerCtx.IssueTitle != nil && *triggerCtx.IssueTitle != "" {
			ctxData["{{issue_title}}"] = *triggerCtx.IssueTitle
		}
	}
	prInstructions := ""
	if run.TriggerType.IsPRTrigger() {
		var prCtx prTriggerContext
		if json.Unmarshal([]byte(run.TriggerContextJSON), &prCtx) == nil {
			ctxData["{{repo}}"] = prCtx.Repo
			ctxData["{{pr_number}}"] = strconv.Itoa(prCtx.PRNumber)
			ctxData["{{pr_title}}"] = prCtx.PRTitle
			ctxData["{{pr_url}}"] = prCtx.PRURL
			ctxData["{{head_sha}}"] = prCtx.HeadSHA
		}
		prInstructions = prAgentPromptInstructions(publicURL)
	}
	return renderAutomationPrompt(snap.PromptTemplate, publicURL, token, run.ID, ctxData, prInstructions)
}

// prAgentPromptInstructions PR 评审线程的代发端点说明（拼进提示词）。
func prAgentPromptInstructions(publicURL string) string {
	return fmt.Sprintf(`## GitHub PR 评审代发端点（系统附加）

本次运行审查 GitHub PR，以下端点以本运行的身份代发（无需也不可提供 GitHub 凭证；
owner/repo/PR 号/commit 均由服务端从运行记录解析，不接受传入）：

- 读取 PR 元数据：GET %s/api/automation-agent/pull-request
- 读取 diff（超过 1MB 截断）：GET %s/api/automation-agent/pull-request/diff
- 读取已有 review 评论：GET %s/api/automation-agent/pull-request/comments
- 提交 review（event=COMMENT|APPROVE|REQUEST_CHANGES，commit 固定为本轮 head）：
  POST %s/api/automation-agent/pull-request/review（{"event":"COMMENT","body":"..."}）
- 更新本运行 check 的标题与说明（结论由运行终态决定）：
  POST %s/api/automation-agent/pull-request/check（{"title":"...","summary":"...","text":"..."})

令牌随本运行结束（含取消）立即失效。
`, publicURL, publicURL, publicURL, publicURL, publicURL)
}

// buildT3LaunchRequest 由运行行与快照组装 launchThread 请求：命令/线程/消息 ID
// 全部用预分配值（重试幂等），标题固定「自动化 · 名称」。
func buildT3LaunchRequest(run *model.AutomationRun, snap automationConfigSnapshot, prompt string) *T3CreateThreadRequest {
	req := &T3CreateThreadRequest{
		CommandId: &run.LaunchCommandID,
		ThreadId:  &run.ThreadID,
		ProjectId: run.T3ProjectID,
		Title:     api.Ptr("自动化 · " + snap.AutomationName),
		Message: api.CreateT3ThreadRequest_Message{
			MessageId: &run.InitialMessageID,
			Text:      prompt,
		},
	}
	if snap.RuntimeMode != "" {
		mode := api.CreateT3ThreadRequestRuntimeMode(snap.RuntimeMode)
		req.RuntimeMode = &mode
	}
	if len(snap.ModelSelection) > 0 {
		var sel api.T3ModelSelection
		if json.Unmarshal(snap.ModelSelection, &sel) == nil {
			req.ModelSelection = &sel
		}
	}
	if len(snap.Workspace) > 0 {
		var ws api.T3ThreadWorkspace
		if json.Unmarshal(snap.Workspace, &ws) == nil && ws.Type != "" {
			req.Workspace = &ws
		}
	}
	return req
}

// ---- 小工具 ----

func parseAutomationSnapshot(raw string) automationConfigSnapshot {
	var snap automationConfigSnapshot
	if json.Unmarshal([]byte(raw), &snap) != nil {
		return automationConfigSnapshot{RuntimeMode: "full-access", PostAction: model.AutomationPostNone}
	}
	if snap.RuntimeMode == "" {
		snap.RuntimeMode = "full-access"
	}
	return snap
}

// newAutomationRunToken 生成 `fsr_` + 32 字节随机 base64url 的运行令牌。
func newAutomationRunToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "fsr_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashRunToken 只存完整令牌（含 fsr_ 前缀）的 SHA-256，与 API Key 同哈希口径。
func hashRunToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func shortRunID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func postActionLabel(action model.AutomationPostAction) string {
	switch action {
	case model.AutomationPostSettle:
		return "settle（收进历史区）"
	case model.AutomationPostArchive:
		return "archive（归档）"
	}
	return string(action)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func nowUTC() time.Time { return time.Now().UTC() }
