package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var pullRequestURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/(\d+)(?:[/?#].*)?$`)

const (
	pullRequestAttachTimeout = 15 * time.Second
	pullRequestSyncTimeout   = 30 * time.Second
)

// AttachIssuePullRequest 把一个 GitHub PR 关联到 Issue。
// 输入只是 PR URL：解析出 owner/repo/number 后立刻向 GitHub 拉取详情填充，
// 拉取失败直接报错不落记录。同一 PR 重复 attach 幂等——返回既有行并顺带刷新。
// 允许跨仓库，repo_full_name 可以是任意 GitHub 仓库，不要求等于项目配置的仓库。
func (s *IssueService) AttachIssuePullRequest(issueID, userID string, req AttachIssuePullRequestRequest) (*IssuePullRequestResponse, error) {
	owner, repo, number, ok := parsePullRequestURL(req.Url)
	if !ok {
		return nil, errs.New(errs.ErrInvalidParams.Code, "无法识别的 PR 链接，仅支持 https://github.com/<owner>/<repo>/pull/<number>")
	}

	issue, project, err := s.loadIssueAndProject(issueID, userID)
	if err != nil {
		return nil, err
	}

	client, credKind, err := s.pullRequestClient(project, owner, repo)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pullRequestAttachTimeout)
	defer cancel()
	pr, err := client.GetPullRequest(ctx, number)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("获取 GitHub PR 失败（%s）: %v%s", credKind.usage(), err, pullRequestAccessHint(err, credKind)))
	}

	now := time.Now().UTC()
	link := &model.IssuePullRequest{
		ID:           uuid.NewString(),
		IssueID:      issue.ID,
		ProjectID:    issue.ProjectID,
		Provider:     model.IssuePullRequestProviderGitHub,
		RepoFullName: owner + "/" + repo,
		Number:       number,
		LinkOrigin:   model.IssuePullRequestLinkOriginManual,
		CreatedAt:    now,
	}
	applyPullRequestData(link, pr, now)

	// 原子 upsert：并发/重复 attach 撞唯一键时由冲突路径刷新既有行，不返回 500。
	if err := s.pullRequestRepo.Upsert(link); err != nil {
		return nil, errs.ErrInternal
	}

	// 冲突路径下 link.ID 是新建值而非既有行主键，重新按唯一键读回规范行。
	stored, err := s.pullRequestRepo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, link.RepoFullName, number)
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := toIssuePullRequestResponse(*stored)
	return &resp, nil
}

// DetachIssuePullRequest 解除 Issue 与某个 PR 的关联（按关联行 id）。
func (s *IssueService) DetachIssuePullRequest(issueID, userID, linkID string) error {
	if _, _, err := s.loadIssueAndProject(issueID, userID); err != nil {
		return err
	}

	if _, err := s.pullRequestRepo.FindByID(issueID, linkID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrPullRequestNotFound
		}
		return errs.ErrInternal
	}
	if err := s.pullRequestRepo.Delete(issueID, linkID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// SyncIssuePullRequests 逐条重新拉取该 Issue 下全部已关联 PR 的最新状态。
// 只更新既有行，不新增、不删除；manual 与 synced 行都会被刷新。
// 每行是独立失败域：单条拉取/保存失败记入 failures 不中断其余行（旧数据保留），
// 客户端构造失败使该 repo 下每行各记一条；整体恒返回 200 形状的 {items, failures}。
func (s *IssueService) SyncIssuePullRequests(issueID, userID string) (*IssuePullRequestSyncResultResponse, error) {
	issue, project, err := s.loadIssueAndProject(issueID, userID)
	if err != nil {
		return nil, err
	}

	links, err := s.pullRequestRepo.ListByIssueID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	result := &IssuePullRequestSyncResultResponse{
		Items:    make([]IssuePullRequestResponse, 0, len(links)),
		Failures: make([]IssuePullRequestSyncFailureItem, 0),
	}
	if len(links) == 0 {
		return result, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), pullRequestSyncTimeout)
	defer cancel()

	clients := make(map[string]gitHubIssueClient)
	clientKinds := make(map[string]pullRequestCredentialKind)
	clientErrs := make(map[string]error)
	now := time.Now().UTC()
	for i := range links {
		link := &links[i]

		client, credKind, err := pullRequestSyncClient(s, clients, clientKinds, clientErrs, project, link)
		if err != nil {
			result.Failures = append(result.Failures, pullRequestSyncFailure(link, err.Error()))
			continue
		}

		pr, err := client.GetPullRequest(ctx, link.Number)
		if err != nil {
			result.Failures = append(result.Failures, pullRequestSyncFailure(link, fmt.Sprintf("拉取失败（%s）: %v%s", credKind.usage(), err, pullRequestAccessHint(err, credKind))))
			continue
		}

		applyPullRequestData(link, pr, now)
		if err := s.pullRequestRepo.SaveSyncedFields(link); err != nil {
			msg := "保存失败"
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				// sync 窗口内被并发 detach——不插回，记一条失败说明。
				msg = "关联已解除"
			case isUniqueConstraintError(err):
				// 仓库改名后归一化撞上既有关联行的唯一键——保留旧行让调用方显式处理。
				msg = "仓库已改名或与既有关联行冲突，请 detach 陈旧行"
			}
			result.Failures = append(result.Failures, pullRequestSyncFailure(link, msg))
			continue
		}
		result.Items = append(result.Items, toIssuePullRequestResponse(*link))
	}

	return result, nil
}

// pullRequestSyncClient 按 repo 复用/缓存客户端；构造错误同样按 repo 缓存，
// 让同 repo 的后续行各自记一条失败而不是重复构造。凭证来源随客户端一并返回
// 并缓存（构造失败时也缓存），供每行失败文案标明本次用的是哪种凭证。
func pullRequestSyncClient(s *IssueService, clients map[string]gitHubIssueClient, clientKinds map[string]pullRequestCredentialKind, clientErrs map[string]error, project *model.Project, link *model.IssuePullRequest) (gitHubIssueClient, pullRequestCredentialKind, error) {
	if client, ok := clients[link.RepoFullName]; ok {
		return client, clientKinds[link.RepoFullName], nil
	}
	if err, failed := clientErrs[link.RepoFullName]; failed {
		return nil, clientKinds[link.RepoFullName], err
	}
	parts := strings.SplitN(link.RepoFullName, "/", 2)
	if len(parts) != 2 || link.Provider != model.IssuePullRequestProviderGitHub {
		err := fmt.Errorf("无法为 %s 构造 GitHub 客户端", link.RepoFullName)
		clientErrs[link.RepoFullName] = err
		return nil, "", err
	}
	client, kind, err := s.pullRequestClient(project, parts[0], parts[1])
	if err != nil {
		clientErrs[link.RepoFullName] = err
		clientKinds[link.RepoFullName] = kind
		return nil, kind, err
	}
	clients[link.RepoFullName] = client
	clientKinds[link.RepoFullName] = kind
	return client, kind, nil
}

// pullRequestSyncFailure 组装单条失败明细：id 为关联行 id，error 前缀带 repo#number 便于定位。
func pullRequestSyncFailure(link *model.IssuePullRequest, reason string) IssuePullRequestSyncFailureItem {
	return IssuePullRequestSyncFailureItem{
		Id:    link.ID,
		Error: fmt.Sprintf("%s#%d: %s", link.RepoFullName, link.Number, reason),
	}
}

func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ListIssuePullRequests 返回 Issue 已关联的 PR 数组，不做归属校验——仅供详情等已完成鉴权的调用方填充。
func (s *IssueService) ListIssuePullRequests(issueID string) ([]IssuePullRequestResponse, error) {
	links, err := s.pullRequestRepo.ListByIssueID(issueID)
	if err != nil {
		return nil, err
	}
	return toIssuePullRequestResponses(links), nil
}

// pullRequestSummariesByIssueIDs 为列表项拉取 PR 聚合计数（一次 GROUP BY，不 N+1）。
func (s *IssueService) pullRequestSummariesByIssueIDs(issues []model.Issue) (map[string]repository.IssuePullRequestSummary, error) {
	issueIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		issueIDs = append(issueIDs, issue.ID)
	}
	return s.pullRequestRepo.SummariesByIssueIDs(issueIDs)
}

// loadIssueAndProject 取出 Issue 并校验调用方对所属项目的访问权，返回两者供后续使用。
func (s *IssueService) loadIssueAndProject(issueID, userID string) (*model.Issue, *model.Project, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errs.ErrIssueNotFound
		}
		return nil, nil, errs.ErrInternal
	}
	project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errs.ErrProjectNotFound
		}
		return nil, nil, errs.ErrInternal
	}
	return issue, project, nil
}

// pullRequestClient 为任意 owner/repo 构造 GitHub 客户端，并返回本次使用的凭证来源。
// 凭证选择集中在 credentials.pullRequestCredential：已配 PR 访问 Token 只用 PR Token，
// 未配则回退项目 Token，项目未配 GitHub 时匿名访问公共仓库；解密失败报错不降级。
func (s *IssueService) pullRequestClient(project *model.Project, owner, repo string) (gitHubIssueClient, pullRequestCredentialKind, error) {
	tokenBytes, kind, appErr := s.credentials.pullRequestCredential(project)
	if appErr != nil {
		return nil, kind, appErr
	}
	return s.newClient(string(tokenBytes), owner, repo), kind, nil
}

// pullRequestAccessHint 对 GitHub 的权限类错误、404 与限流追加排查提示。
// GitHub 对「PR 不存在」和「凭证无权访问该仓库」都返回 404，文案必须同时保留两种可能；
// 限流错误（HTTP 403）是 *gh.RateLimitError/*gh.AbuseRateLimitError，不走 *gh.ErrorResponse，
// 且限流时 PR 与凭证都正常，必须给不同的提示，不能把用户引向查权限。
const pullRequestAccessHintText = "；PR 不存在或当前凭证无权访问该仓库，请确认仓库访问范围与 Pull requests 读权限"
const pullRequestRateLimitHintText = "；GitHub API 限流，请稍后重试"
const pullRequestRateLimitAnonymousHintText = "（匿名访问限 60 次/小时，配置 Token 可提高限额）"

func pullRequestAccessHint(err error, kind pullRequestCredentialKind) string {
	var rateLimitErr *gh.RateLimitError
	var abuseErr *gh.AbuseRateLimitError
	if errors.As(err, &rateLimitErr) || errors.As(err, &abuseErr) {
		// 次级限流对已配置 Token 的请求同样触发，限额说明只在匿名时适用。
		if kind == pullRequestCredentialAnonymous {
			return pullRequestRateLimitHintText + pullRequestRateLimitAnonymousHintText
		}
		return pullRequestRateLimitHintText
	}
	var errResp *gh.ErrorResponse
	if !errors.As(err, &errResp) || errResp.Response == nil {
		return ""
	}
	switch errResp.Response.StatusCode {
	case 401, 403, 404:
		return pullRequestAccessHintText
	default:
		return ""
	}
}

// parsePullRequestURL 解析 GitHub PR 链接，容忍 http、www 前缀与 /files、query、fragment 等尾部。
func parsePullRequestURL(raw string) (owner, repo string, number int, ok bool) {
	m := pullRequestURLPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", "", 0, false
	}
	if !isGitHubPathName(m[1]) || !isGitHubPathName(m[2]) {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return "", "", 0, false
	}
	return m[1], m[2], n, true
}

// isGitHubPathName 排除纯点段与点开头结尾的 owner/repo："." / ".." 会被 HTTP
// 客户端做路径归一化，把请求改写到别的路径上（还带着项目 token）。
func isGitHubPathName(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.HasSuffix(s, ".")
}

// applyPullRequestData 把 GitHub PR 详情刷进关联行（状态与合并时间一并重置，closed→open 复活也能纠正回来）。
// repo_full_name 归一到 GitHub 返回的 canonical 名——GitHub 仓库名不区分大小写，
// 但唯一索引区分，直接用 URL 原文会让 Owner/Repo 与 owner/repo 落成两行。
func applyPullRequestData(link *model.IssuePullRequest, pr *gh.PullRequest, now time.Time) {
	if fullName := pr.GetBase().GetRepo().GetFullName(); fullName != "" {
		link.RepoFullName = fullName
	}
	link.Title = pr.GetTitle()
	if htmlURL := pr.GetHTMLURL(); htmlURL != "" {
		link.HTMLURL = htmlURL
	}
	link.State = pullRequestStateFromGitHub(pr)
	link.IsDraft = pr.GetDraft()
	link.AuthorLogin = pr.GetUser().GetLogin()
	link.HeadRef = pr.GetHead().GetRef()
	link.BaseRef = pr.GetBase().GetRef()
	link.MergedAt = nil
	if pr.MergedAt != nil {
		mergedAt := pr.MergedAt.Time.UTC()
		link.MergedAt = &mergedAt
	}
	link.ClosedAt = nil
	if pr.ClosedAt != nil {
		closedAt := pr.ClosedAt.Time.UTC()
		link.ClosedAt = &closedAt
	}
	link.SyncedAt = now
	link.UpdatedAt = now
}

// pullRequestStateFromGitHub 映射 PR 状态：GitHub closed 且 merged_at 非空视为 merged。
func pullRequestStateFromGitHub(pr *gh.PullRequest) model.IssuePullRequestState {
	if pr.GetState() != "closed" {
		return model.IssuePullRequestStateOpen
	}
	if pr.MergedAt != nil {
		return model.IssuePullRequestStateMerged
	}
	return model.IssuePullRequestStateClosed
}

func toIssuePullRequestResponse(link model.IssuePullRequest) IssuePullRequestResponse {
	resp := IssuePullRequestResponse{
		Id:           link.ID,
		IssueId:      link.IssueID,
		Provider:     link.Provider,
		RepoFullName: link.RepoFullName,
		Number:       link.Number,
		HtmlUrl:      link.HTMLURL,
		Title:        link.Title,
		State:        link.State,
		IsDraft:      link.IsDraft,
		AuthorLogin:  link.AuthorLogin,
		HeadRef:      link.HeadRef,
		BaseRef:      link.BaseRef,
		LinkOrigin:   link.LinkOrigin,
		SyncedAt:     formatTime(link.SyncedAt),
		CreatedAt:    formatTime(link.CreatedAt),
		UpdatedAt:    formatTime(link.UpdatedAt),
	}
	if link.MergedAt != nil {
		value := formatTime(link.MergedAt.UTC())
		resp.MergedAt = &value
	}
	if link.ClosedAt != nil {
		value := formatTime(link.ClosedAt.UTC())
		resp.ClosedAt = &value
	}
	return resp
}

func toIssuePullRequestResponses(links []model.IssuePullRequest) []IssuePullRequestResponse {
	resp := make([]IssuePullRequestResponse, 0, len(links))
	for _, link := range links {
		resp = append(resp, toIssuePullRequestResponse(link))
	}
	return resp
}
