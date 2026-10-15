package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"gorm.io/gorm"
)

// PR 评审的 Agent 代发端点（INT-93）：agent 用运行令牌读写自己那一轮的 PR。
// owner/repo/PR 号/SHA/check id 一律取自运行行，请求不接受这些输入；GitHub
// 凭证（安装令牌）由服务端持有，agent 拿不到。

// prAgentDiffMaxBytes diff 截断阈值（1MB）。
const prAgentDiffMaxBytes = 1 << 20

// loadPRRun 代发端点的公共前置：令牌存活的运行、PR 触发、dispatched/running。
// requireLive 已在中间件保证令牌未消费（结束/取消即 40106）。
func (s *AutomationAgentService) loadPRRun(ctx context.Context, runID string) (*model.AutomationRun, error) {
	run, err := s.runs.FindByID(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrRunTokenInvalid
		}
		return nil, errs.ErrInternal
	}
	if !run.TriggerType.IsPRTrigger() ||
		(run.Status != model.AutomationRunDispatched && run.Status != model.AutomationRunRunning) {
		return nil, errs.ErrRunNotPRReview
	}
	return run, nil
}

// prGitHub 返回注入的 GitHub 访问面（未配置时报错）。
func (s *AutomationAgentService) prGitHub() (githubapp.PullRequestGitHub, error) {
	if s.pr == nil {
		return nil, errs.New(errs.ErrInternal.Code, "GitHub App 未配置")
	}
	return s.pr, nil
}

// AgentPullRequest GET /api/automation-agent/pull-request
func (s *AutomationAgentService) AgentPullRequest(ctx context.Context, identity *RunTokenIdentity) (*api.AgentPullRequestDetail, error) {
	run, err := s.loadPRRun(ctx, identity.RunID)
	if err != nil {
		return nil, err
	}
	gh, err := s.prGitHub()
	if err != nil {
		return nil, err
	}
	pr, err := gh.GetPullRequest(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "读取 PR 元数据失败: "+shortenGitHubError(err))
	}
	return &api.AgentPullRequestDetail{
		RepoFullName: run.RepoFullName,
		PrNumber:     run.PRNumber,
		HeadSha:      run.HeadSHA,
		Title:        pr.Title,
		Body:         api.Ptr(pr.Body),
		HtmlUrl:      pr.HTMLURL,
		BaseRef:      api.Ptr(pr.BaseRef),
		HeadRef:      api.Ptr(pr.HeadRef),
		Draft:        api.Ptr(pr.Draft),
	}, nil
}

// AgentPullRequestDiff GET /api/automation-agent/pull-request/diff（>1MB 截断）。
func (s *AutomationAgentService) AgentPullRequestDiff(ctx context.Context, identity *RunTokenIdentity) (*api.AgentPullRequestDiff, error) {
	run, err := s.loadPRRun(ctx, identity.RunID)
	if err != nil {
		return nil, err
	}
	gh, err := s.prGitHub()
	if err != nil {
		return nil, err
	}
	diff, err := gh.GetPullRequestDiff(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "读取 PR diff 失败: "+shortenGitHubError(err))
	}
	truncated := false
	if len(diff) > prAgentDiffMaxBytes {
		diff = diff[:prAgentDiffMaxBytes] + "\n... (diff 超过 1MB，已截断)"
		truncated = true
	}
	return &api.AgentPullRequestDiff{Content: diff, Truncated: truncated}, nil
}

// AgentPullRequestComments GET /api/automation-agent/pull-request/comments
func (s *AutomationAgentService) AgentPullRequestComments(ctx context.Context, identity *RunTokenIdentity) ([]api.AgentPullRequestComment, error) {
	run, err := s.loadPRRun(ctx, identity.RunID)
	if err != nil {
		return nil, err
	}
	gh, err := s.prGitHub()
	if err != nil {
		return nil, err
	}
	comments, err := gh.ListReviewComments(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "读取 PR 评论失败: "+shortenGitHubError(err))
	}
	out := make([]api.AgentPullRequestComment, 0, len(comments))
	for i := range comments {
		c := comments[i]
		item := api.AgentPullRequestComment{
			Id:        int(c.ID),
			User:      c.User,
			Body:      c.Body,
			Path:      api.Ptr(c.Path),
			CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if c.InReplyToID != nil {
			id := int(*c.InReplyToID)
			item.InReplyToId = &id
		}
		out = append(out, item)
	}
	return out, nil
}

// AgentSubmitPullRequestReview POST /api/automation-agent/pull-request/review
// commit_id 固定取本运行 head SHA。与 webhook 取消共用 per-PR 锁，锁内再读
// 运行行：synchronize 取消后不得再提交 COMMENT/APPROVE/REQUEST_CHANGES。
func (s *AutomationAgentService) AgentSubmitPullRequestReview(ctx context.Context, identity *RunTokenIdentity, req *api.AgentPullRequestReviewRequest) (*api.AgentPullRequestReviewResult, error) {
	run, err := s.loadPRRun(ctx, identity.RunID)
	if err != nil {
		return nil, err
	}
	gh, err := s.prGitHub()
	if err != nil {
		return nil, err
	}
	switch api.AgentPullRequestReviewRequestEvent(req.Event) {
	case "COMMENT", "APPROVE", "REQUEST_CHANGES":
	default:
		return nil, errs.New(errs.ErrInvalidParams.Code, "event 必须是 COMMENT / APPROVE / REQUEST_CHANGES")
	}
	do := func() error {
		return s.submitReviewIfLive(ctx, gh, run, req)
	}
	if s.prReview == nil {
		if err := do(); err != nil {
			return nil, err
		}
		return &api.AgentPullRequestReviewResult{Submitted: true}, nil
	}
	var out error
	s.prReview.withPRLock(run.RepoFullName, run.PRNumber, func() {
		out = do()
	})
	if out != nil {
		return nil, out
	}
	return &api.AgentPullRequestReviewResult{Submitted: true}, nil
}

func (s *AutomationAgentService) submitReviewIfLive(ctx context.Context, gh githubapp.PullRequestGitHub, run *model.AutomationRun, req *api.AgentPullRequestReviewRequest) error {
	fresh, err := s.runs.FindByID(ctx, run.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrRunTokenInvalid
		}
		return errs.ErrInternal
	}
	if !fresh.TriggerType.IsPRTrigger() ||
		(fresh.Status != model.AutomationRunDispatched && fresh.Status != model.AutomationRunRunning) {
		return errs.ErrRunNotPRReview
	}
	if err := gh.SubmitReview(ctx, fresh.InstallationID, fresh.RepoFullName, fresh.PRNumber, githubapp.ReviewInput{
		Event:    string(req.Event),
		Body:     api.Deref(req.Body),
		CommitID: fresh.HeadSHA,
	}); err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "提交 review 失败: "+shortenGitHubError(err))
	}
	return nil
}

// AgentUpdateCheck POST /api/automation-agent/pull-request/check：只更新本运行
// check 的 title/summary/text；结论由运行终态决定（同步路径写）。写 GitHub 前
// 再读一次运行行：synchronize 取消后不得把 check 打回 in_progress。有 PR 锁时
// 与评论/check 同步串行；写完若已终态则置 sync_pending 让同步覆盖。
func (s *AutomationAgentService) AgentUpdateCheck(ctx context.Context, identity *RunTokenIdentity, req *api.AgentCheckUpdateRequest) error {
	run, err := s.loadPRRun(ctx, identity.RunID)
	if err != nil {
		return err
	}
	if run.CheckRunID == nil {
		return errs.New(errs.ErrRunNotPRReview.Code, "本运行的 check run 尚未创建，请稍后重试")
	}
	gh, err := s.prGitHub()
	if err != nil {
		return err
	}
	do := func() error {
		return s.updateCheckIfLive(ctx, gh, run, req)
	}
	if s.prReview == nil {
		return do()
	}
	var out error
	s.prReview.withPRLock(run.RepoFullName, run.PRNumber, func() {
		out = do()
	})
	return out
}

func (s *AutomationAgentService) updateCheckIfLive(ctx context.Context, gh githubapp.PullRequestGitHub, run *model.AutomationRun, req *api.AgentCheckUpdateRequest) error {
	fresh, err := s.runs.FindByID(ctx, run.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrRunTokenInvalid
		}
		return errs.ErrInternal
	}
	if !fresh.TriggerType.IsPRTrigger() ||
		(fresh.Status != model.AutomationRunDispatched && fresh.Status != model.AutomationRunRunning) {
		return errs.ErrRunNotPRReview
	}
	if fresh.CheckRunID == nil {
		return errs.New(errs.ErrRunNotPRReview.Code, "本运行的 check run 尚未创建，请稍后重试")
	}
	in := githubapp.CheckUpdate{
		Status:  githubapp.CheckInProgress,
		Title:   api.Deref(req.Title),
		Summary: api.Deref(req.Summary),
		Text:    api.Deref(req.Text),
	}
	if in.Title == "" {
		in.Title = checkNameForModel(fresh.ModelName)
	}
	if err := gh.UpdateCheckRun(ctx, fresh.InstallationID, fresh.RepoFullName, *fresh.CheckRunID, in); err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "更新 check 失败: "+shortenGitHubError(err))
	}
	after, err := s.runs.FindByID(ctx, run.ID)
	if err != nil {
		return nil
	}
	if after.Status != model.AutomationRunDispatched && after.Status != model.AutomationRunRunning {
		_ = markPRReviewPendingTx(s.db, after, time.Now().UTC())
	}
	return nil
}

func shortenGitHubError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i > 0 && len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
