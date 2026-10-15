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
// commit_id 固定取本运行 head SHA。
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
	if err := gh.SubmitReview(ctx, run.InstallationID, run.RepoFullName, run.PRNumber, githubapp.ReviewInput{
		Event:    string(req.Event),
		Body:     api.Deref(req.Body),
		CommitID: run.HeadSHA,
	}); err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "提交 review 失败: "+shortenGitHubError(err))
	}
	return &api.AgentPullRequestReviewResult{Submitted: true}, nil
}

// AgentUpdateCheck POST /api/automation-agent/pull-request/check：只更新本运行
// check 的 title/summary/text；结论由运行终态决定（同步路径写）。
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
	in := githubapp.CheckUpdate{
		Status:  githubapp.CheckInProgress,
		Title:   api.Deref(req.Title),
		Summary: api.Deref(req.Summary),
		Text:    api.Deref(req.Text),
	}
	if in.Title == "" {
		in.Title = checkNameForModel(run.ModelName)
	}
	if err := gh.UpdateCheckRun(ctx, run.InstallationID, run.RepoFullName, *run.CheckRunID, in); err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "更新 check 失败: "+shortenGitHubError(err))
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
