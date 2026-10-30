package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PR 审查调度循环（INT-135）：单进程一个 10s tick + 非阻塞 kick。
// 每轮依次：到期防抖消费并开轮 → 排队提升 → 派发 → 超时 → 取消运行的线程
// 中断 → GitHub 同步 → 归档 → 工作区清理 → 旧线程墓碑清理。各阶段独立容错；
// 网络调用都在事务外。投递失败对账在独立 goroutine 里低频跑，见
// review_redelivery.go。

const reviewSchedulerInterval = 10 * time.Second

// 每 tick 批量上限：防抖消费 50、其余阶段 20、派发认领 4。准备失败重试
// 不设批量上限，见 retryFailedPreparations。
const (
	reviewDebounceBatch = 50
	reviewStageBatch    = 20
	reviewDispatchBatch = 4
)

// reviewNetworkStageBudget 调度循环里带串行外部读的阶段（防抖消费逐行读
// PR 实况、准备失败重试逐行读 T3 投影）的单轮耗时预算：与同步阶段单 PR
// 2 分钟同量级，保证上游变慢时一个 tick 仍大体有界，超时/取消中断/同步/
// 回收不会被拖到几十分钟后。超预算的剩余行本来就还在库里，下个 tick 再处理。
const reviewNetworkStageBudget = 60 * time.Second

// Start 启动审查调度循环（幂等：重复调用不叠加第二个循环）。Forgejo 轮询
// 补漏挂在同一组循环里（INT-167 任务 3）：共用 cancel，tick 频率更低。
func (s *ReviewService) Start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	cancel := make(chan struct{})
	kickCh := make(chan struct{}, 1)
	s.cancel = cancel
	s.kickCh = kickCh
	s.mu.Unlock()
	go s.reviewLoop(cancel, kickCh)
	go s.redeliveryLoop(cancel)
	go s.forgejoPollLoop(cancel)
}

// Stop 停止调度循环；幂等，未启动时为空操作。
func (s *ReviewService) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.kickCh = nil
	s.mu.Unlock()
	if cancel != nil {
		close(cancel)
	}
}

func (s *ReviewService) reviewLoop(cancel <-chan struct{}, kickCh <-chan struct{}) {
	ticker := time.NewTicker(reviewSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cancel:
			return
		case now := <-ticker.C:
			s.reviewTick(now.UTC())
		case <-kickCh:
			s.reviewTick(time.Now().UTC())
		}
	}
}

// redeliveryLoop 投递失败对账的独立低频循环：不与 10s 调度 tick 抢执行权
// （GitHub 变慢时对账会长时间占住网络调用，不能拖住防抖消费/派发/超时）。
// 启动即先跑一轮（进程刚起来时最需要补上停机期间漏掉的投递），之后每
// reviewRedeliverySweepEvery 一轮。与 reviewLoop 共用同一个 cancel。
func (s *ReviewService) redeliveryLoop(cancel <-chan struct{}) {
	ticker := time.NewTicker(reviewRedeliverySweepEvery)
	defer ticker.Stop()
	s.reconcileWebhookDeliveries(time.Now().UTC())
	for {
		select {
		case <-cancel:
			return
		case now := <-ticker.C:
			s.reconcileWebhookDeliveries(now.UTC())
		}
	}
}

// reviewTick 每轮顺序执行：到期防抖消费并开轮 → 排队提升 → 派发 →
// 工作区准备失败代发重试 → 超时 → 取消运行的线程中断 → GitHub 同步 → 归档 →
// 工作区清理 → 旧线程墓碑清理。各阶段独立容错，互不打断。重试排在超时之前：
// 重试成功会重置 deadline，别让同一 tick 的旧 deadline 抢先落 timed_out。
// 归档在工作区清理之前：删除必须以归档完成为前提。
func (s *ReviewService) reviewTick(now time.Time) {
	s.processDueDebounces(now)
	s.promoteQueuedRuns(now)
	s.dispatchRuns(now)
	s.retryFailedPreparations(now)
	s.markTimeouts(now)
	s.interruptCancelledRuns(now)
	s.syncPendingPRs(now)
	s.archiveRuns(now)
	s.cleanupWorktrees(now)
	s.cleanupLegacyThreads(now)
}

// cleanupLegacyThreads 处理通用自动化下线时留下的在途线程墓碑：停线程、
// 归档，成功即删行；失败有限重试后放弃（只留日志），避免墓碑无限累积。
func (s *ReviewService) cleanupLegacyThreads(now time.Time) {
	if s.t3 == nil {
		return
	}
	var rows []model.LegacyAutomationThread
	if err := s.db.Order("id ASC").Limit(reviewStageBatch).Find(&rows).Error; err != nil {
		s.logger.Error("查询旧自动化线程墓碑失败", zap.Error(err))
		return
	}
	for _, row := range rows {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, stopErr := s.t3.StopThread(ctx, row.UserID, row.EnvironmentID, row.ThreadID)
		var archiveErr error
		if stopErr == nil {
			archiveErr = s.t3.ArchiveThread(ctx, row.UserID, row.EnvironmentID, row.ThreadID)
		}
		cancel()
		if stopErr != nil || archiveErr != nil {
			if row.Attempts+1 >= reviewWorktreeMaxTries {
				s.logger.Warn("旧自动化线程清理失败，放弃墓碑",
					zap.String("thread", row.ThreadID), zap.Error(stopErr), zap.Error(archiveErr))
				s.db.Where("id = ?", row.ID).Delete(&model.LegacyAutomationThread{})
				continue
			}
			s.db.Model(&model.LegacyAutomationThread{}).Where("id = ?", row.ID).
				Update("attempts", row.Attempts+1)
			continue
		}
		s.db.Where("id = ?", row.ID).Delete(&model.LegacyAutomationThread{})
	}
}

// processDueDebounces 到期防抖行逐行短事务消费：条件删除（fire_at 未推后、
// head 未更新）→ 复核项目仍启用/动作仍勾选/仓库归属未变 → openRoundTx。
// 顺带清理超龄 delivery 幂等账。
func (s *ReviewService) processDueDebounces(now time.Time) {
	var rows []model.ReviewDebounce
	if err := s.db.Where("fire_at <= ?", now).Order("fire_at ASC").Limit(reviewDebounceBatch).
		Find(&rows).Error; err != nil {
		s.logger.Error("查询到期防抖行失败", zap.Error(err))
		return
	}
	start := time.Now()
	for i := range rows {
		if time.Since(start) > reviewNetworkStageBudget {
			s.logger.Info("防抖消费到达阶段网络预算，剩余行下轮处理", zap.Int("remaining", len(rows)-i))
			break
		}
		s.consumeDebounce(&rows[i], now)
	}
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		_, err := s.deliveries.DeleteOlderThanTx(tx, now.Add(-reviewDeliveryRetention))
		return err
	})
	// 重投次数账（INT-146）按同一保留期清理；guid 账只在重投后短期内有意义，
	// 丢了顶多按投递列表重新推近期次数。
	if _, err := s.redeliveries.DeleteOlderThan(s.db, now.Add(-reviewDeliveryRetention)); err != nil {
		s.logger.Error("清理 webhook 重投次数账失败", zap.Error(err))
	}
}

// deterministicReviewAccessErr 实况读/轮询的确定性失败：仓库或 PR 已删
// （404）、凭证被拒或吊销（401/403）、集成行缺失/kind 不符、密文损坏——
// 每轮重试结果相同，按瞬时故障无限推后只会让行/项目卡死刷错；网络超时
// 与 5xx 不在此列（推后重试才有意义）。
func deterministicReviewAccessErr(err error) bool {
	if forge.IsRateLimit(err) {
		// 限流 403（GitHub 主/二次限流正是回 403）是瞬时失败——推后重试，
		// 不删行不停用。
		return false
	}
	return forge.IsNotFound(err) || forge.IsForbidden(err) || forge.IsUnauthorized(err) ||
		errors.Is(err, gorm.ErrRecordNotFound) ||
		// 密文损坏两处同口径：Forgejo 集成密文与 GitHub App 部署配置密文
		// （解密失败重投/重试只会得到同一结果，fast-ship-bot 第 36 轮 P2）。
		errors.Is(err, errForgeIntegrationDecrypt) || errors.Is(err, errGitHubAppDecrypt) ||
		// App 身份未配置同属永久配置态（webhook-secret-only 部署等），与
		// 评论唤醒分类同口径（fast-ship-bot 第 40 轮 Note）。
		errors.Is(err, githubapp.ErrAppNotConfigured)
}

// debounceInstallationID 防抖实况读的安装 id 兜底：GitHub 投递缺
// installation 快照（代理剥字段）时防抖行内为 0——直接拿 0 换 token 会
// 404 并被确定性分支删行静默丢推送（fast-ship-bot 第 36 轮 P2）。先沿用
// 总账快照（openRoundTx 对 0 有「保留旧值」兜底，但新加的实读让那条路走
// 不到），仍缺才按仓库解析安装（评论唤醒同款）。
func (s *ReviewService) debounceInstallationID(ctx context.Context, row *model.ReviewDebounce) (int64, error) {
	if row.InstallationID != 0 || row.IntegrationID != model.ForgeIntegrationBuiltinGitHubID {
		return row.InstallationID, nil
	}
	var ledger model.ReviewPullRequest
	lErr := s.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
		row.ProjectID, row.RepoFullName, row.PRNumber, row.IntegrationID).
		First(&ledger).Error
	if lErr == nil && ledger.InstallationID != 0 {
		return ledger.InstallationID, nil
	}
	if lErr != nil && !errors.Is(lErr, gorm.ErrRecordNotFound) {
		return 0, lErr
	}
	return s.resolveGitHubInstallation(ctx, row.RepoFullName)
}

// consumeDebounce 一行防抖的消费：先在事务外读 PR 实况——防抖行可能是已
// 合并/已关闭 PR 的旧账（重投的 closed 投递被有意排除、payload 又是旧快照），
// PR 已关闭或远端 head 与防抖行不一致则删行不开轮。读 PR 失败不开轮也不删
// 行（暂时性故障不能当成「已关闭」），但把 fire_at 推后一个防抖窗口：否则
// 一条永久读不到的行（仓库转走/PR 不可见）会一直排在本 tick 批量队首，把
// 后续正常防抖行挡在 50 条名额之外。事务内条件删除防重入，复核项目仍启
// 用/动作仍勾选/仓库一致后同事务开轮。
func (s *ReviewService) consumeDebounce(row *model.ReviewDebounce, now time.Time) {
	// 单次 PR 实况读 10s：这是「读一次 PR 状态」的检查而非数据传输，超时只
	// 意味着本轮不开轮（fire_at 推后），10s 足够，别占满整个阶段预算。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	installationID, instErr := s.debounceInstallationID(ctx, row)
	var client forge.ReviewClient
	var pr *forge.PullRequest
	var err error
	if instErr == nil {
		client, err = s.reviewClient(ctx, row.IntegrationID, row.RepoFullName, installationID)
		if err == nil {
			pr, err = client.GetPullRequest(ctx, row.RepoFullName, row.PRNumber)
		}
	} else {
		err = instErr
	}
	cancel()
	if err != nil {
		// 确定性失败（仓库/PR 已删、凭证被拒 401/403、集成不存在、密文损坏）
		// 每轮重试结果相同——直接删行不再推后，否则防抖行永远消费不掉还按
		// 周期刷错；后续新事件会重建防抖行。
		if deterministicReviewAccessErr(err) {
			s.logger.Warn("防抖实况读确定性失败，删除防抖行",
				zap.String("repo", row.RepoFullName), zap.Int("pr", row.PRNumber), zap.Error(err))
			if delErr := s.db.Where(
				"project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND head_sha = ? AND fire_at <= ?",
				row.ProjectID, row.RepoFullName, row.PRNumber, row.IntegrationID, row.HeadSHA, now,
			).Delete(&model.ReviewDebounce{}).Error; delErr != nil {
				s.logger.Error("删除确定性失败的防抖行失败", zap.Error(delErr))
			}
			return
		}
		s.logger.Warn("防抖消费前读取 PR 失败，推后重试",
			zap.String("repo", row.RepoFullName), zap.Int("pr", row.PRNumber), zap.Error(err))
		// 推后用当前时刻而不是 tick 开始的 now：一个阶段预算内本批次可能已
		// 跑了很久，慢批次结束时按 now 推后的时刻可能已经过期，行会立刻再次
		// 被选中继续失败。
		pushTo := time.Now().UTC().Add(reviewDebounceSeconds * time.Second)
		if retryErr := s.db.Where(
			"project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND head_sha = ? AND fire_at <= ?",
			row.ProjectID, row.RepoFullName, row.PRNumber, row.IntegrationID, row.HeadSHA, now,
		).Model(&model.ReviewDebounce{}).
			Update("fire_at", pushTo).Error; retryErr != nil {
			s.logger.Error("推后防抖行失败",
				zap.String("repo", row.RepoFullName), zap.Int("pr", row.PRNumber), zap.Error(retryErr))
		}
		return
	}
	// PR 已关闭/合并或远端 head 已偏（新 webhook 尚未落账）→ 删行不开轮；
	// 新 head 的防抖行由它自己的 webhook 重建。
	reviewable := prReviewable(pr) && strings.EqualFold(pr.HeadSHA, row.HeadSHA)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where(
			"project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND head_sha = ? AND fire_at <= ?",
			row.ProjectID, row.RepoFullName, row.PRNumber, row.IntegrationID, row.HeadSHA, now,
		).Delete(&model.ReviewDebounce{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // 已被更新（新 head 覆盖）或已消费
		}
		if !reviewable {
			return nil
		}
		var project model.Project
		if err := tx.Where("id = ?", row.ProjectID).First(&project).Error; err != nil {
			return err
		}
		var settings model.ProjectReviewSettings
		if err := tx.Where("project_id = ?", row.ProjectID).First(&settings).Error; err != nil {
			return err
		}
		// 复核：仍启用、动作仍勾选、(集成, 仓库)（enabled 设置行的归一快照，
		// 与 webhook 反查、syncOnePR 准入复核同口径）仍是该防抖行对应的键。
		// 两个键写库时都是归一值，直接等值比较；不回读项目行的 live 字段——
		// 快照是本项目审查对象的唯一真相。
		if !settings.Enabled || !reviewTriggerEnabled(&settings, row.Action) {
			return nil
		}
		if settings.RepoFullName != row.RepoFullName || settings.IntegrationID != row.IntegrationID {
			return nil
		}
		// 传兜底解析后的 installationID：首次审查（无总账）时 row 内为 0，
		// openRoundTx 的「0 保留旧值」兜底无从生效，账本与运行行会以 0 派发
		// 失败（fast-ship-bot 第 37 轮 P2）。
		_, err := s.openRoundTx(tx, &project, &settings,
			row.RepoFullName, row.PRNumber, row.HeadSHA, row.Action,
			row.PRTitle, row.PRURL, row.BaseRef, row.HeadRef, installationID, nil, now)
		return err
	})
	if err != nil {
		s.logger.Error("消费防抖行失败",
			zap.String("repo", row.RepoFullName), zap.Int("pr", row.PRNumber), zap.Error(err))
	}
}

// promoteQueuedRuns 每用户在途 <5 时按创建时间把最老 queued 提升为
// dispatched，deadline 按提升时刻 + kind 超时重设。
func (s *ReviewService) promoteQueuedRuns(now time.Time) {
	var userIDs []string
	if err := s.db.Model(&model.ReviewRun{}).
		Where("status = ?", model.ReviewRunQueued).
		Distinct().Limit(reviewStageBatch).Pluck("user_id", &userIDs).Error; err != nil {
		s.logger.Error("查询排队用户失败", zap.Error(err))
		return
	}
	for _, userID := range userIDs {
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			return s.promoteQueuedRunsTx(tx, userID, now)
		}); err != nil {
			s.logger.Error("提升排队运行失败", zap.String("user", userID), zap.Error(err))
		}
	}
}

// reviewDispatchClaimTimeout 认领后超过这个时长仍未确认建线程（进程在
// 认领与派发之间退出/崩溃）的运行允许重新认领。窗口只覆盖从未签发过令牌
// 的运行：令牌是随机值、库中只存 SHA-256 哈希，进程重启后无法恢复原值；
// 令牌存在即证明 launchRun 至少走到过签发点，已建线程持有的旧令牌一旦
// 被新签发覆盖就再也鉴权不上——这类运行不再重试，交给 deadline 超时兜底。
const reviewDispatchClaimTimeout = 5 * time.Minute

// dispatchRuns 认领未启动的 dispatched 行并事务外 launchRun。
// 启动请求不可变重放：threadId/commandId/messageId 在运行行上预分配，
// 重试重放同一请求不会建出第二个线程；令牌只在运行从未签发过时才新签。
func (s *ReviewService) dispatchRuns(now time.Time) {
	reclaimBefore := now.Add(-reviewDispatchClaimTimeout)
	// 认领条件：dispatched 且未认领，或认领超窗仍未确认建线程，且从未签发
	// 令牌（NOT EXISTS）。令牌存在的不重认领——见上面常量注释。
	const pending = `status = ?
		AND (dispatch_started_at IS NULL OR (dispatched_at IS NULL AND dispatch_started_at < ?))
		AND NOT EXISTS (SELECT 1 FROM review_run_tokens t WHERE t.run_id = review_runs.id)`
	var runs []model.ReviewRun
	if err := s.db.
		Where(pending, model.ReviewRunDispatched, reclaimBefore).
		Order("created_at ASC").Limit(reviewDispatchBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询待派发运行失败", zap.Error(err))
		return
	}
	for _, run := range runs {
		res := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND "+pending, run.ID, model.ReviewRunDispatched, reclaimBefore).
			Updates(map[string]any{"dispatch_started_at": now, "updated_at": now})
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		s.launchRun(context.Background(), run.ID, now)
	}
}

// markTimeouts dispatched/running 过 deadline → timed_out + 消费令牌 +
// 置 PR sync_pending + 通知。
func (s *ReviewService) markTimeouts(now time.Time) {
	var runs []model.ReviewRun
	if err := s.db.Where("status IN ? AND deadline_at <= ?",
		[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}, now).
		Limit(reviewStageBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询超时运行失败", zap.Error(err))
		return
	}
	for i := range runs {
		run := &runs[i]
		applied := false
		err := s.db.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&model.ReviewRun{}).
				Where("id = ? AND status IN ? AND deadline_at <= ?", run.ID,
					[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}, now).
				Updates(map[string]any{
					"status":      model.ReviewRunTimedOut,
					"error_code":  "timeout",
					"finished_at": now,
					"updated_at":  now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil
			}
			applied = true
			if err := tx.Model(&model.ReviewRunToken{}).
				Where("run_id = ? AND consumed_at IS NULL", run.ID).
				Update("consumed_at", now).Error; err != nil {
				return err
			}
			return s.markPRSyncPendingByRepoTx(tx, run.IntegrationID, run.ProjectID, run.RepoFullName, run.PRNumber, now)
		})
		if err != nil {
			s.logger.Error("标记审查超时失败", zap.String("run", run.ID), zap.Error(err))
			continue
		}
		if !applied {
			continue // 已被结束回报/取消抢先落终态：不发超时通知
		}
		s.publishReviewNotification(run, model.NotificationReviewRunTimeout, "运行超过截止时间仍未回报结果", now)
	}
}

// interruptCancelledRuns 被取消且已派发的运行补一次线程中断（StopThread）。
// 只发一次：先条件占 stop_attempted_at 标记再调用。
func (s *ReviewService) interruptCancelledRuns(now time.Time) {
	if s.t3 == nil {
		return
	}
	var runs []model.ReviewRun
	if err := s.db.Where("status = ? AND dispatch_started_at IS NOT NULL AND stop_attempted_at IS NULL",
		model.ReviewRunCancelled).
		Limit(reviewStageBatch).Find(&runs).Error; err != nil {
		s.logger.Error("查询待中断运行失败", zap.Error(err))
		return
	}
	for _, run := range runs {
		res := s.db.Model(&model.ReviewRun{}).
			Where("id = ? AND stop_attempted_at IS NULL", run.ID).
			Update("stop_attempted_at", now)
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		if run.ThreadID == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := s.t3.StopThread(ctx, run.UserID, run.EnvironmentID, run.ThreadID)
		cancel()
		if err != nil {
			// 中断是尽力而为：线程可能从未建成（launch 未确认）。
			s.logger.Info("取消运行的线程中断未成功（忽略）",
				zap.String("run", run.ID), zap.Error(err))
		}
	}
}
