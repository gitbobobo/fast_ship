package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AutomationAgentService 面向自动化运行令牌的 Agent 端点（INT-90）：
// 回报（started/finished）、线程读取、通知发布。凭证不是 JWT 也不是 API Key，
// 而是派发时写进提示词的一次性运行令牌（fsr_）。

type AutomationAgentService struct {
	db      *gorm.DB
	runs    *repository.AutomationRunRepository
	tokens  *repository.AutomationRunTokenRepository
	threads *T3ThreadsService
	sink    *NotificationSink
	logger  *zap.Logger

	// pr GitHub App 访问面（INT-93 PR 代发端点用）；未配置时 PR 端点报错。
	pr githubapi.GitHubClient
	// prReview 与取消/同步共用 per-PR 锁；可 nil（单测不注入时只做重读校验）。
	prReview *PRReviewService
	// kick 结束回报后踢调度循环提升排队（可 nil）。
	kick func()
}

// SetPRGitHub 注入 GitHub App 访问面（真实客户端或测试 fake）。
func (s *AutomationAgentService) SetPRGitHub(gh githubapi.GitHubClient) { s.pr = gh }

// SetPRReview 绑定 PR 评审协调面，让 agent check 更新与取消/同步串行。
func (s *AutomationAgentService) SetPRReview(pr *PRReviewService) { s.prReview = pr }

// SetKick 绑定调度循环 kick（运行结束后立刻提升排队）。
func (s *AutomationAgentService) SetKick(fn func()) { s.kick = fn }

func NewAutomationAgentService(
	db *gorm.DB,
	runs *repository.AutomationRunRepository,
	tokens *repository.AutomationRunTokenRepository,
	threads *T3ThreadsService,
	sink *NotificationSink,
	logger *zap.Logger,
) *AutomationAgentService {
	return &AutomationAgentService{
		db: db, runs: runs, tokens: tokens, threads: threads, sink: sink, logger: logger,
	}
}

// RunTokenIdentity 从令牌推出的身份：运行与其归属用户。
type RunTokenIdentity struct {
	UserID string
	RunID  string
}

// ResolveRunToken 校验 Bearer 令牌并推导身份。requireLive=false 供回报端点：
// 已消费的令牌仍可重放完全相同的结束回报（直到 expires_at）；requireLive=true
// 供读线程/发通知：消费即刻失效。
func (s *AutomationAgentService) ResolveRunToken(ctx context.Context, rawToken string, requireLive bool) (*RunTokenIdentity, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil, errs.ErrRunTokenInvalid
	}
	token, err := s.tokens.FindByHash(ctx, hashRunToken(rawToken))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrRunTokenInvalid
		}
		return nil, errs.ErrInternal
	}
	now := time.Now().UTC()
	if now.After(token.ExpiresAt) {
		return nil, errs.ErrRunTokenInvalid
	}
	if requireLive && token.ConsumedAt != nil {
		return nil, errs.ErrRunTokenInvalid
	}
	run, err := s.runs.FindByID(ctx, token.RunID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrRunTokenInvalid
		}
		return nil, errs.ErrInternal
	}
	if requireLive && run.Status == model.AutomationRunCancelled {
		return nil, errs.ErrRunTokenInvalid
	}
	return &RunTokenIdentity{UserID: run.UserID, RunID: run.ID}, nil
}

// Report 受理运行回报。started 幂等；finished 在同一事务内消费令牌并登记
// 后置操作，允许结束先于开始；超时后首次结束保持 timed_out。
func (s *AutomationAgentService) Report(ctx context.Context, identity *RunTokenIdentity, req *api.AutomationRunReportRequest) (*api.AutomationRunReportResult, error) {
	switch req.Phase {
	case api.Started:
		return s.reportStarted(ctx, identity.RunID)
	case api.Finished:
		return s.reportFinished(ctx, identity.RunID, req)
	default:
		return nil, errs.ErrInvalidParams
	}
}

func (s *AutomationAgentService) reportStarted(ctx context.Context, runID string) (*api.AutomationRunReportResult, error) {
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.AutomationRun
		if err := tx.Where("id = ?", runID).First(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrAutomationRunNotFound
			}
			return err
		}
		switch run.Status {
		case model.AutomationRunDispatched, model.RunStatusPendingRun:
			// agent 能回报=线程确实在跑：顺带确认派发（含派发结果未确认、
			// 甚至还没被 worker 认领的运行——回报先到的竞态按已派发收敛）。
			cols := map[string]any{
				"status":        string(model.AutomationRunRunning),
				"started_at":    now,
				"dispatched_at": now,
			}
			if run.DispatchedAt != nil {
				delete(cols, "dispatched_at")
			}
			n, err := s.runs.UpdateColumnsWhereTx(tx, runID,
				"status IN (?, ?)",
				[]any{string(model.AutomationRunDispatched), string(model.RunStatusPendingRun)}, cols)
			if err != nil {
				return err
			}
			if n == 0 {
				return errs.ErrAutomationRunReportConflict
			}
			return nil
		case model.AutomationRunRunning:
			return nil // 幂等重放
		default:
			return errs.ErrAutomationRunReportConflict
		}
	})
	if err != nil {
		return nil, err
	}
	return &api.AutomationRunReportResult{RunId: runID, Status: string(model.AutomationRunRunning), Replay: false}, nil
}

func (s *AutomationAgentService) reportFinished(ctx context.Context, runID string, req *api.AutomationRunReportRequest) (*api.AutomationRunReportResult, error) {
	now := time.Now().UTC()
	summary := api.Deref(req.Summary)
	errorCode := api.Deref(req.ErrorCode)
	errorMessage := api.Deref(req.ErrorMessage)

	replay := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run model.AutomationRun
		if err := tx.Where("id = ?", runID).First(&run).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrAutomationRunNotFound
			}
			return err
		}

		// 令牌已消费 ⇒ 此前已有结束回报被受理：仅完全相同的三元组可重放。
		var tokenRow model.AutomationRunToken
		if err := tx.Where("run_id = ?", runID).First(&tokenRow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrRunTokenInvalid
			}
			return err
		}
		if tokenRow.ConsumedAt != nil {
			if runFinishedMatches(&run, summary, errorCode, errorMessage) {
				replay = true
				return nil
			}
			return errs.ErrAutomationRunReportConflict
		}

		// 首次结束回报（含超时后的迟到回报）。终态归属：
		// 已超时保持 timed_out（迟到结束不能改成 completed）；
		// 其余按回报内容：带 error_code 记 failed，否则 completed。
		finalStatus := model.AutomationRunCompleted
		if run.Status == model.AutomationRunTimedOut {
			finalStatus = model.AutomationRunTimedOut
		} else if errorCode != "" {
			finalStatus = model.AutomationRunFailed
		}

		cols := map[string]any{
			"status":        string(finalStatus),
			"finished_at":   now,
			"summary":       summary,
			"error_code":    errorCode,
			"error_message": errorMessage,
		}
		n, err := s.runs.UpdateColumnsWhereTx(tx, runID,
			"status IN (?, ?, ?, ?)",
			[]any{
				string(model.AutomationRunDispatched),
				string(model.AutomationRunRunning),
				string(model.RunStatusPendingRun),
				string(model.AutomationRunTimedOut),
			}, cols)
		if err != nil {
			return err
		}
		if n == 0 {
			return errs.ErrAutomationRunReportConflict
		}

		// 同事务消费令牌：成功后本运行一切读取/通知立即失效。
		nTok, err := s.tokens.ConsumeTx(tx, runID, now)
		if err != nil {
			return err
		}
		if nTok == 0 {
			return errs.ErrAutomationRunReportConflict
		}

		// 登记后置操作：仅正常结束、配置了后置且线程确认建成。超时路径的后置
		// 已由协调循环按 post_on_timeout 处理，迟到回报不重复登记。
		if finalStatus != model.AutomationRunTimedOut {
			snap := parseAutomationSnapshot(run.ConfigSnapshotJSON)
			if snap.PostAction != model.AutomationPostNone && run.DispatchedAt != nil {
				if err := registerPostPending(tx, runID, now); err != nil {
					return err
				}
			}
		}

		// PR 评审（INT-93）：终态落库后置同步标记（事务外推评论/check）。
		if err := markPRReviewPendingTx(tx, &run, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if replay {
		// 重放不改任何状态；给调用方返回当前终态。
		run, err := s.runs.FindByID(ctx, runID)
		if err != nil {
			return nil, errs.ErrInternal
		}
		return &api.AutomationRunReportResult{RunId: runID, Status: string(run.Status), Replay: true}, nil
	}
	if s.kick != nil {
		s.kick()
	}
	// 落库后的实际状态（迟到回报保持 timed_out）。
	run, err := s.runs.FindByID(ctx, runID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return &api.AutomationRunReportResult{RunId: runID, Status: string(run.Status), Replay: false}, nil
}

// runFinishedMatches 完全相同重放的判定：summary/error_code/error_message 一致
// 且结束时间已登记（即此前确有结束回报被受理）。
func runFinishedMatches(run *model.AutomationRun, summary, errorCode, errorMessage string) bool {
	return run.FinishedAt != nil &&
		run.Summary == summary &&
		run.ErrorCode == errorCode &&
		run.ErrorMessage == errorMessage
}

// ListThreads 线程聚合读取（口径同 JWT /api/threads），另加可选过滤。
func (s *AutomationAgentService) ListThreads(ctx context.Context, identity *RunTokenIdentity, envID, projectID, status string, settled *bool) (*T3ThreadListResponse, error) {
	list, err := s.threads.List(ctx, identity.UserID)
	if err != nil {
		return nil, err
	}
	if envID == "" && projectID == "" && status == "" && settled == nil {
		return list, nil
	}
	filtered := make([]T3ThreadSummary, 0, len(list.Items))
	for _, item := range list.Items {
		if envID != "" && item.EnvironmentId != envID {
			continue
		}
		if projectID != "" && item.ProjectId != projectID {
			continue
		}
		if status != "" && string(item.Status) != status {
			continue
		}
		if settled != nil && item.Settled != *settled {
			continue
		}
		filtered = append(filtered, item)
	}
	list.Items = filtered
	return list, nil
}

// ThreadDetail 单线程详情 + items 分页：默认最后 limit 条、页内正序；
// before_item_id 取该条目之前的上一页；边界条目已消失 40918。
func (s *AutomationAgentService) ThreadDetail(ctx context.Context, identity *RunTokenIdentity, envID, threadID string, limit int, beforeItemID string) (*T3ThreadDetail, error) {
	detail, err := s.threads.Detail(ctx, identity.UserID, envID, threadID)
	if err != nil {
		return nil, err
	}
	items, err := paginateT3Items(detail.Items, limit, beforeItemID)
	if err != nil {
		return nil, err
	}
	detail.Items = items
	return detail, nil
}

// ThreadItemDetail 单条目完整正文（转发生在 service 内部，鉴权已完成）。
func (s *AutomationAgentService) ThreadItemDetail(ctx context.Context, identity *RunTokenIdentity, envID, threadID, itemID string) (*T3ThreadItem, error) {
	return s.threads.ItemDetail(ctx, identity.UserID, envID, threadID, itemID)
}

// paginateT3Items 按契约切分页窗口（items 输入输出均为时间正序）。
func paginateT3Items(items []T3ThreadItem, limit int, beforeItemID string) ([]T3ThreadItem, error) {
	if limit <= 0 {
		limit = automationAgentThreadDefaultLimit
	}
	if limit > automationAgentThreadMaxLimit {
		limit = automationAgentThreadMaxLimit
	}
	if beforeItemID == "" {
		if len(items) > limit {
			return items[len(items)-limit:], nil
		}
		return items, nil
	}
	boundary := -1
	for i := range items {
		if items[i].Id == beforeItemID {
			boundary = i
			break
		}
	}
	if boundary < 0 {
		return nil, errs.ErrPageBoundaryInvalid
	}
	start := boundary - limit
	if start < 0 {
		start = 0
	}
	return items[start:boundary], nil
}

const (
	automationAgentThreadDefaultLimit = 50
	automationAgentThreadMaxLimit     = 100
)

// PublishNotification 以运行身份发布站内通知。kind 固定 agent：Agent 通知
// 不能冒充系统派发失败等系统 kind。环境/线程缺省挂到本运行。
func (s *AutomationAgentService) PublishNotification(ctx context.Context, identity *RunTokenIdentity, req *api.AgentNotificationRequest) error {
	run, err := s.runs.FindByID(ctx, identity.RunID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrAutomationRunNotFound
		}
		return errs.ErrInternal
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return errs.New(errs.ErrInvalidParams.Code, "通知标题不能为空")
	}
	problemKey := strings.TrimSpace(req.ProblemKey)
	if problemKey == "" {
		return errs.New(errs.ErrInvalidParams.Code, "problem_key 不能为空")
	}
	stateKey := strings.TrimSpace(req.StateKey)
	if stateKey == "" {
		return errs.New(errs.ErrInvalidParams.Code, "state_key 不能为空")
	}
	envID := run.EnvironmentID
	if req.EnvironmentId != nil && strings.TrimSpace(*req.EnvironmentId) != "" {
		envID = strings.TrimSpace(*req.EnvironmentId)
	}
	threadID := run.ThreadID
	if req.ThreadId != nil && strings.TrimSpace(*req.ThreadId) != "" {
		threadID = strings.TrimSpace(*req.ThreadId)
	}
	body := ""
	if req.Body != nil {
		body = *req.Body
	}
	return s.sink.Publish(NotificationInput{
		UserID:        identity.UserID,
		Kind:          model.NotificationAgent,
		EnvironmentID: envID,
		ThreadID:      threadID,
		RunID:         run.ID,
		ProblemKey:    problemKey,
		StateKey:      stateKey,
		Title:         title,
		Body:          body,
	})
}
