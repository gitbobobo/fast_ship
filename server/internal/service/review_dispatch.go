package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 派发（INT-135）：签发运行令牌 → 渲染系统提示词 → launchThread。全部网络
// 调用在事务外；dispatch_started_at 认领保证单次派发，launchSent 语义决定
// 失败处置（未发出=明确失败，发出未确认=等超时）。

// reviewTokenGrace 运行令牌在运行 deadline 之后的缓冲（回报可能踩点到达）。
const reviewTokenGrace = 5 * time.Minute

// issueRunToken 生成 fsr_ 令牌并把 SHA-256 落库。调度认领面已保证同一运行
// 只走到一次签发点（已签发的运行不再被重认领，见 dispatchRuns 的
// NOT EXISTS 条件）；这里的 upsert 只作兜底——真发生重复派发时旧令牌失效，
// 由超时兜底，绝不静默放行两套令牌。
func (s *ReviewService) issueRunToken(run *model.ReviewRun, now time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := "fsr_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	row := &model.ReviewRunToken{
		RunID:     run.ID,
		TokenHash: hex.EncodeToString(sum[:]),
		CreatedAt: now,
		ExpiresAt: run.DeadlineAt.Add(reviewTokenGrace),
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "run_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"token_hash", "created_at", "expires_at", "consumed_at"}),
	}).Create(row).Error; err != nil {
		return "", err
	}
	return token, nil
}

// launchRun 一次运行的事务外派发。任何先决失败都把运行落成
// failed/dispatch_failed + 通知；RPC 发出但结果未达保持 dispatched 等超时。
func (s *ReviewService) launchRun(ctx context.Context, runID string, now time.Time) {
	// 与删除项目的清理互斥：删除会先取消非终态运行并停线程，启动不能在
	// 清理之后才把线程建成（那时运行行已被 CASCADE 删掉）。
	unlock := s.lockProject(projectIDForRun(s.db, runID))
	defer unlock()
	var run model.ReviewRun
	if err := s.db.Where("id = ?", runID).First(&run).Error; err != nil {
		return
	}
	if run.Status != model.ReviewRunDispatched {
		return
	}
	fail := func(msg string) {
		s.failRunDispatch(run.ID, "dispatch_failed", msg, now)
	}

	// 项目绑定：t3_project_id 以绑定表真值为准（组合里快照值只做展示）。
	var binding model.ProjectT3Binding
	if err := s.db.Where("project_id = ? AND environment_id = ?", run.ProjectID, run.EnvironmentID).
		First(&binding).Error; err != nil {
		fail("环境未绑定该项目或绑定缺失")
		return
	}
	if s.t3 == nil {
		fail("T3 访问面未配置")
		return
	}
	publicURL := ""
	if s.publicURL != nil {
		publicURL = s.publicURL.PublicURL(ctx)
	}
	if publicURL == "" {
		fail("public_url 未配置")
		return
	}

	// 建线程前最后一道关卡：PR 实况必须还是 open（覆盖「排队期间 PR 被关、
	// closed 投递丢失」——closed 有意不进重投，对账不会补）。已关闭/合并
	// 直接落 cancelled/pr_closed 终态，不建线程。检查必须在签发令牌之前：
	// 只有从未签发的运行才会被 dispatchRuns 的认领超窗重新认领——读 PR 失败
	// 时同样不落终态、不签令牌，留给下一轮。确定性失败（仓库/PR 已删、凭证
	// 被拒/吊销、集成行缺失、密文损坏）每轮重认领结果相同：按消费防抖/轮询
	// 的同一口径直接落终态报真实原因，不空转到 deadline 误报超时。
	prCtx, prCancel := context.WithTimeout(ctx, 30*time.Second)
	client, clientErr := s.reviewClient(prCtx, run.IntegrationID, run.RepoFullName, run.InstallationID)
	var pr *forge.PullRequest
	if clientErr == nil {
		pr, clientErr = client.GetPullRequest(prCtx, run.RepoFullName, run.PRNumber)
	}
	prCancel()
	if clientErr != nil {
		switch {
		case forge.IsNotFound(clientErr):
			// GitHub/Forgejo 对无权限资源也回 404（凭证吊销、仓库转私有、
			// 安装被卸）——仓库级复核消歧：仓库可读而 PR 404 = PR 真已删，
			// 同「PR 已关」终局不另发通知；仓库同样确定性读不到 = 访问权
			// 丧失，报 access_denied 真实原因；探测本身瞬时失败不证明任何
			// 一边，等重认领。
			var probeErr error
			if client != nil {
				repoCtx, repoCancel := context.WithTimeout(ctx, 10*time.Second)
				probeErr = client.GetRepository(repoCtx, run.RepoFullName)
				repoCancel()
			}
			switch {
			case probeErr == nil:
				s.cancelRunPRClosed(&run, now)
			case deterministicReviewAccessErr(probeErr):
				s.failRunDispatch(run.ID, "access_denied", clientErr.Error(), now)
			default:
				s.logger.Warn("派发前仓库级复核失败，运行等待重新认领",
					zap.String("run", run.ID), zap.Error(probeErr))
			}
		case deterministicReviewAccessErr(clientErr):
			s.failRunDispatch(run.ID, "access_denied", clientErr.Error(), now)
		default:
			s.logger.Warn("派发前读取 PR 失败，运行等待重新认领",
				zap.String("run", run.ID), zap.Error(clientErr))
		}
		return
	}
	if !prReviewable(pr) {
		s.cancelRunPRClosed(&run, now)
		return
	}

	token, err := s.issueRunToken(&run, now)
	if err != nil {
		fail("签发运行令牌失败")
		return
	}
	prompt := renderReviewPrompt(&run, publicURL, token)

	// 模型选择：options 按 id 排序映射成 T3 [{id,value}]。
	selection := &api.T3ModelSelection{InstanceId: run.InstanceID, Model: run.Model}
	var opts []api.ReviewModelOption
	if run.ModelOptionsJSON != "" {
		_ = json.Unmarshal([]byte(run.ModelOptionsJSON), &opts)
	}
	for _, o := range opts {
		selection.Options = append(selection.Options, api.T3ModelOption{Id: o.Id, Value: o.Value})
	}

	// 工作区：代码审查开 worktree（基于 head 分支），视觉审查用主检出。
	// 工作区路径不在 launch 时解析——createThread 确认时 worktree 常尚未建好，
	// 回收阶段按线程投影的 worktreePath/branch 真值处理（见 cleanupRunWorktree）。
	workspace := &api.T3ThreadWorkspace{Type: "root"}
	if run.Kind == model.ReviewKindCode {
		startOrigin := true
		workspace = &api.T3ThreadWorkspace{Type: "worktree", BaseRef: &run.HeadRef, StartFromOrigin: &startOrigin}
	}

	req := &T3CreateThreadRequest{
		ProjectId:      binding.T3ProjectID,
		ThreadId:       &run.ThreadID,
		CommandId:      &run.LaunchCommandID,
		Title:          api.Ptr(reviewThreadTitle(&run)),
		ModelSelection: selection,
		RuntimeMode:    api.Ptr(api.FullAccess),
		Workspace:      workspace,
		Message: api.CreateT3ThreadRequest_Message{
			MessageId: &run.InitialMessageID,
			Text:      prompt,
		},
	}
	result, launchSent, err := s.t3.CreateThreadForAgent(ctx, run.UserID, run.EnvironmentID, req)
	if err != nil {
		if !launchSent {
			s.failRunDispatch(run.ID, "dispatch_failed", err.Error(), now)
			return
		}
		// 已发出未确认：保留 dispatched 等 deadline 超时兜底。
		s.logger.Warn("审查线程启动结果未确认", zap.String("run", run.ID), zap.Error(err))
		return
	}

	// 线程确实建成了：不论运行是否在启动准备期间被新 SHA 取消/超时，都要把
	// launch 目标与工作区根目录落库——归档与工作区回收都以「线程已确认」为
	// 前提。Agent 可能先鉴权把 dispatched_at 写上了，所以元数据按「字段仍为
	// 空」单独更新，dispatched_at 只在为空时补。
	if err := s.db.Model(&model.ReviewRun{}).
		Where("id = ? AND workspace_root = ''", run.ID).
		Updates(map[string]any{
			"t3_project_id":  binding.T3ProjectID,
			"thread_id":      result.ThreadId,
			"workspace_root": binding.WorkspaceRoot,
			"updated_at":     now,
		}).Error; err != nil {
		s.logger.Error("更新审查运行 launch 快照失败", zap.String("run", run.ID), zap.Error(err))
		return
	}
	if err := s.db.Model(&model.ReviewRun{}).
		Where("id = ? AND dispatched_at IS NULL", run.ID).
		Updates(map[string]any{"dispatched_at": now, "updated_at": now}).Error; err != nil {
		s.logger.Error("更新审查运行派发状态失败", zap.String("run", run.ID), zap.Error(err))
		return
	}
}

// projectIDForRun 读运行所属项目（读取失败返回空串，锁退化为按空串串行）。
func projectIDForRun(db *gorm.DB, runID string) string {
	var projectID string
	_ = db.Model(&model.ReviewRun{}).Where("id = ?", runID).Select("project_id").Scan(&projectID).Error
	return projectID
}

// failRunDispatch 派发失败终态：failed + 消费令牌 + 置 PR sync_pending + 通知。
func (s *ReviewService) failRunDispatch(runID, code, msg string, now time.Time) {
	var run model.ReviewRun
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.ReviewRun{}).
			Where("id = ? AND status IN ?", runID,
				[]model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}).
			Updates(map[string]any{
				"status":        model.ReviewRunFailed,
				"error_code":    code,
				"error_message": msg,
				"finished_at":   now,
				"updated_at":    now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Where("id = ?", runID).First(&run).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ReviewRunToken{}).
			Where("run_id = ? AND consumed_at IS NULL", runID).
			Update("consumed_at", now).Error; err != nil {
			return err
		}
		return s.markPRSyncPendingByRepoTx(tx, run.IntegrationID, run.ProjectID, run.RepoFullName, run.PRNumber, now)
	})
	s.publishReviewNotification(&run, model.NotificationReviewDispatchFailed, msg, now)
}

// cancelRunPRClosed PR 已关闭时把运行落终态：cancelled + error_code=pr_closed
// + 消费令牌 + 置 PR sync_pending。与 failRunDispatch 同构但不是派发失败——
// PR 关闭是正常终局，不发 dispatch_failed 通知。
func (s *ReviewService) cancelRunPRClosed(run *model.ReviewRun, now time.Time) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.ReviewRun{}).
			Where("id = ? AND status IN ?", run.ID,
				[]model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}).
			Updates(map[string]any{
				"status":      model.ReviewRunCancelled,
				"error_code":  "pr_closed",
				"finished_at": now,
				"updated_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Model(&model.ReviewRunToken{}).
			Where("run_id = ? AND consumed_at IS NULL", run.ID).
			Update("consumed_at", now).Error; err != nil {
			return err
		}
		return s.markPRSyncPendingByRepoTx(tx, run.IntegrationID, run.ProjectID, run.RepoFullName, run.PRNumber, now)
	})
	if err != nil {
		s.logger.Error("PR 关闭收尾运行失败", zap.String("run", run.ID), zap.Error(err))
	}
}

// markPRSyncPendingByRepoTx 本 (项目, 集成) 总账置 sync_pending（不重取项目
// ——行已存在）。主键含 project_id 与 integration_id：其他键的同仓库账本不置。
func (s *ReviewService) markPRSyncPendingByRepoTx(tx *gorm.DB, integrationID, projectID, repo string, prNumber int, now time.Time) error {
	return tx.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			projectID, repo, prNumber, integrationID).
		Updates(map[string]any{
			"sync_pending": true,
			"sync_epoch":   gorm.Expr("sync_epoch + 1"),
			"updated_at":   now,
		}).Error
}

// publishReviewNotification 审查故障通知（dedup 按 run+kind；无运行时不发）。
func (s *ReviewService) publishReviewNotification(run *model.ReviewRun, kind model.NotificationKind, problem string, now time.Time) {
	if s.sink == nil || run.ID == "" {
		return
	}
	prURL := run.PRURL
	if prURL == "" {
		prURL = run.RepoFullName + " PR #" + strconv.Itoa(run.PRNumber)
	}
	err := s.sink.Publish(NotificationInput{
		UserID:        run.UserID,
		Kind:          kind,
		Title:         reviewNotificationTitle(kind, prURL),
		Body:          problem,
		EnvironmentID: run.EnvironmentID,
		ThreadID:      run.ThreadID,
		ProblemKey:    run.ID,
	})
	if err != nil {
		s.logger.Warn("写审查通知失败", zap.String("run", run.ID), zap.Error(err))
	}
}

func reviewNotificationTitle(kind model.NotificationKind, prURL string) string {
	switch kind {
	case model.NotificationReviewRunTimeout:
		return "PR 审查超时：" + prURL
	case model.NotificationReviewDispatchFailed:
		return "PR 审查派发失败：" + prURL
	case model.NotificationReviewCleanupFailed:
		return "PR 审查工作区回收失败：" + prURL
	}
	return "PR 审查：" + prURL
}
