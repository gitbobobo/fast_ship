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
	workspace := &api.T3ThreadWorkspace{Type: "root"}
	if run.Kind == model.ReviewKindCode {
		startOrigin := true
		workspace = &api.T3ThreadWorkspace{Type: "worktree", BaseRef: &run.HeadRef, StartFromOrigin: &startOrigin}
		// 记录 launch 前的已知 worktree 路径——只在紧随 launch 的有界窗口内
		// 做「新增即本次」差集；窗口过后基线释放，回收阶段不再猜路径。
		if paths, listErr := s.t3.ListWorktreePaths(ctx, run.UserID, run.EnvironmentID, binding.T3ProjectID); listErr == nil {
			s.worktreeMu.Lock()
			s.worktreeBaseline[run.ID] = paths
			s.worktreeMu.Unlock()
		} else {
			s.logger.Warn("worktree 基线列举失败（本运行无法证明工作区归属）",
				zap.String("run", run.ID), zap.Error(listErr))
		}
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

	// 代码审查：launch 确认后有界窗口内解析本次工作区路径（被取消的运行也要
	// 解析，否则回收阶段无法证明归属、只能记失败）。
	if run.Kind == model.ReviewKindCode {
		s.resolveWorktreePath(ctx, &run, binding.T3ProjectID)
	}
}

// reviewWorktreeResolveTries/Interval launch 确认后工作区路径解析的重试窗口：
// T3 建 worktree 可能略滞后于 thread 创建确认，给 3 次、间隔 1.5s。
const (
	reviewWorktreeResolveTries    = 3
	reviewWorktreeResolveInterval = 1500 * time.Millisecond
)

// resolveWorktreePath launch 后的工作区解析：launch 前基线差集，在有限重试
// 窗口内写 worktree_path；窗口结束（无论成败）统一释放基线。解析不到就
// 保持空——回收阶段不再猜路径，空路径即「无法证明归属」。
func (s *ReviewService) resolveWorktreePath(ctx context.Context, run *model.ReviewRun, t3ProjectID string) {
	defer s.releaseWorktreeState(run.ID)
	if _, ok := s.baselineFor(run.ID); !ok {
		return // launch 前列举已失败，差集无从算起
	}
	for attempt := 0; attempt < reviewWorktreeResolveTries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(reviewWorktreeResolveInterval):
			}
		}
		paths, err := s.t3.ListWorktreePaths(ctx, run.UserID, run.EnvironmentID, t3ProjectID)
		if err != nil {
			s.logger.Warn("launch 后列举 worktree 失败", zap.String("run", run.ID), zap.Error(err))
			continue
		}
		if path := s.diffWorktreePath(run.ID, paths); path != "" {
			s.db.Model(&model.ReviewRun{}).
				Where("id = ? AND worktree_path = ''", run.ID).
				Updates(map[string]any{"worktree_path": path, "updated_at": time.Now().UTC()})
			return
		}
	}
}

// baselineFor 取 launch 前基线（只读）。
func (s *ReviewService) baselineFor(runID string) ([]string, bool) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	b, ok := s.worktreeBaseline[runID]
	return b, ok
}

// releaseWorktreeState 释放一个运行的进程内工作区状态（差集基线 + 回收
// 重试计数）：解析成功、解析失败、终态回收结束都必须调用，防泄漏。
func (s *ReviewService) releaseWorktreeState(runID string) {
	s.worktreeMu.Lock()
	defer s.worktreeMu.Unlock()
	delete(s.worktreeBaseline, runID)
	delete(s.worktreeTries, runID)
}

// projectIDForRun 读运行所属项目（读取失败返回空串，锁退化为按空串串行）。
func projectIDForRun(db *gorm.DB, runID string) string {
	var projectID string
	_ = db.Model(&model.ReviewRun{}).Where("id = ?", runID).Select("project_id").Scan(&projectID).Error
	return projectID
}

// diffWorktreePath 计算 launch 后新增的唯一 worktree 路径；无新增或多个返回空。
func (s *ReviewService) diffWorktreePath(runID string, current []string) string {
	s.worktreeMu.Lock()
	baseline, ok := s.worktreeBaseline[runID]
	s.worktreeMu.Unlock()
	if !ok {
		return ""
	}
	base := map[string]struct{}{}
	for _, p := range baseline {
		base[p] = struct{}{}
	}
	var added []string
	for _, p := range current {
		if _, seen := base[p]; !seen {
			added = append(added, p)
		}
	}
	if len(added) == 1 {
		return added[0]
	}
	return ""
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
		return s.markPRSyncPendingByRepoTx(tx, run.ProjectID, run.RepoFullName, run.PRNumber, now)
	})
	s.publishReviewNotification(&run, model.NotificationReviewDispatchFailed, msg, now)
}

// markPRSyncPendingByRepoTx 本项目总账置 sync_pending（不重取项目——行已
// 存在）。主键含 project_id：其他项目的同仓库账本不置。
func (s *ReviewService) markPRSyncPendingByRepoTx(tx *gorm.DB, projectID, repo string, prNumber int, now time.Time) error {
	return tx.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", projectID, repo, prNumber).
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
