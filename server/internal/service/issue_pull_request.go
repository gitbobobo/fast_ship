package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/forges"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"sync"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
	"gorm.io/gorm"
)

var (
	pullRequestURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/(\d+)(?:[/?#].*)?$`)

	// forgejoPullRequestURLPattern 自托管 Forgejo/Gitea 实例的 PR 链接（INT-181）：
	// 非 github.com 主机 + /{owner}/{repo}/pulls/{n}，容忍 http/https、显式端口
	// 与 /files、query、fragment 等尾部（风格对齐 pullRequestURLPattern）。
	// scheme 与 authority 单独捕获：authority 段交给 url.Parse 归一（IPv6 字面量、
	// 端口、误写的 userinfo 都能正确拆出 host），host 只用于集成查找，不用于发起
	// 请求；scheme 参与主机的缺省端口归一（http→80、https→443）。
	forgejoPullRequestURLPattern = regexp.MustCompile(`(?i)^(https?)://([^/?#]+)/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pulls/(\d+)(?:[/?#].*)?$`)
)

const (
	pullRequestAttachTimeout = 15 * time.Second
	pullRequestSyncTimeout   = 30 * time.Second
	// pullRequestSyncConcurrency 是 sync 拉取远端 PR 的并发上限（与看板
	// 批量关单的 batchCloseGitHubConcurrency 同口径）：上游限流温和，N 条
	// 关联的总耗时约为单条的数倍以内而非线性累加。
	pullRequestSyncConcurrency = 4
)

// pullRequestLink 解析后的 PR 链接：Provider 决定后续取数路径；forgejo 行的
// HostKey 是 PR URL 主机的归一键（forges.HostKey，与 forge_integrations.host_key
// 同口径），github 行恒空串。
type pullRequestLink struct {
	Provider string
	Owner    string
	Repo     string
	Number   int
	HostKey  string
}

// issueForgeSource 关联 PR 链路的 Forgejo 集成凭证面最小接口（INT-181）：签名
// 与 forge.ForgejoSource 同形，生产接 ForgeClientSource（同一实现同时满足
// resolver 与本接口），测试可注入桩。只授权 forge_integrations 的机器人 PAT，
// 不接项目/用户级凭证。
type issueForgeSource interface {
	ForgejoClient(ctx context.Context, integrationID string) (httpClient *http.Client, apiBaseURL, token string, err error)
	// ForgejoClientFor 接受调用方已读出的集成行：sync 的主机校验与客户端构
	// 建用同一份行数据，消除「校验读一次、构建再读一次」之间集成被改指的竞
	// 态窗口（INT-181 R8）。
	ForgejoClientFor(ctx context.Context, row *model.ForgeIntegration) (httpClient *http.Client, apiBaseURL, token string, err error)
}

// 编译期钉住 ForgeClientSource 满足关联 PR 链路的凭证面。
var _ issueForgeSource = (*ForgeClientSource)(nil)

// forgePRReader 关联 PR 链路对 Forgejo 平台的最小读取面（INT-181）：forge.ReviewClient
// 的 GetPullRequest 子集，测试桩不必实现整个审查方法面。
type forgePRReader interface {
	GetPullRequest(ctx context.Context, repoFullName string, number int) (*forge.PullRequest, error)
}

// AttachIssuePullRequest 把一个 GitHub 或 Forgejo PR 关联到 Issue（INT-181 起
// 支持双 provider）。输入只是 PR URL：github.com 链接沿用 GitHub 路径（凭证与
// 报错口径不变）；其他主机的 /pulls/{n} 链接按主机键找部署级 Forgejo 集成，
// 用集成机器人 PAT 拉取详情并落集成快照。解析/查集成/拉取失败直接报错不落
// 记录。同一 PR 重复 attach 幂等——返回既有行并顺带刷新。允许跨仓库，
// repo_full_name 可以是任意仓库，不要求等于项目配置的仓库。
func (s *IssueService) AttachIssuePullRequest(issueID, userID string, req AttachIssuePullRequestRequest) (*IssuePullRequestResponse, error) {
	parsed, ok := parsePullRequestLink(req.Url)
	if !ok {
		return nil, errs.New(errs.ErrInvalidParams.Code, "无法识别的 PR 链接，支持 https://github.com/<owner>/<repo>/pull/<number> 或 http(s)://<host>/<owner>/<repo>/pulls/<number>")
	}

	issue, project, err := s.loadIssueAndProject(issueID, userID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pullRequestAttachTimeout)
	defer cancel()
	now := time.Now().UTC()
	link := &model.IssuePullRequest{
		ID:         uuid.NewString(),
		IssueID:    issue.ID,
		ProjectID:  issue.ProjectID,
		Number:     parsed.Number,
		LinkOrigin: model.IssuePullRequestLinkOriginManual,
		CreatedAt:  now,
	}
	link.HostKey = parsed.HostKey // github 行恒空串：主机在唯一键内，跨实例同名 PR 天然并存

	switch parsed.Provider {
	case model.IssuePullRequestProviderForgejo:
		link.Provider = model.IssuePullRequestProviderForgejo
		link.RepoFullName = parsed.Owner + "/" + parsed.Repo
		// 一次读取项目绑定的集成行，主机校验与客户端构建用同一份数据（与
		// sync 的 R8 口径对齐）：读取与构建之间集成被改指不再可能。
		integration, err := s.forgejoIntegrationForProject(project, parsed.HostKey)
		if err != nil {
			return nil, err
		}
		link.ForgeIntegrationID = integration.ID
		if s.forgeSource == nil {
			return nil, errors.New("Forgejo 集成凭证来源未配置")
		}
		httpClient, apiBase, token, err := s.forgeSource.ForgejoClientFor(ctx, integration)
		if err != nil {
			s.logger.Error("Forgejo attach 读取集成失败", zap.Error(err))
			// 本地凭证面故障（行已删/kind 不符/解密失败/读库瞬时故障）是配置或存
			// 储问题，不是 Forgejo 上游失败——按 50000 报而非 50206，客户端不会
			// 误按网关故障重试，与 sync 对同一根因返回 200+failures 的口径一致。
			return nil, errs.New(errs.ErrInternal.Code, classifyForgeIntegrationReadErr(err).Error())
		}
		pr, err := s.newForgejoPRReader(httpClient, apiBase, token).GetPullRequest(ctx, link.RepoFullName, parsed.Number)
		if err != nil {
			return nil, errs.New(errs.ErrForgeAPI.Code, s.forgejoPullRequestFailureText("获取 Forgejo PR", err))
		}
		applyForgePullRequestData(link, pr, now)
	default:
		// GitHub 既有路径原样保留：凭证选择集中在 credentials.pullRequestCredential。
		link.Provider = model.IssuePullRequestProviderGitHub
		link.RepoFullName = parsed.Owner + "/" + parsed.Repo
		client, credKind, err := s.pullRequestClient(project)
		if err != nil {
			return nil, err
		}
		pr, err := client.GetPullRequest(ctx, 0, link.RepoFullName, parsed.Number)
		if err != nil {
			return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("获取 GitHub PR 失败（%s）: %v%s", credKind.usage(), err, pullRequestAccessHint(err, credKind)))
		}
		applyPullRequestData(link, pr, now)
	}

	// 原子 upsert：并发/重复 attach 撞唯一键（含 host_key）时由冲突路径刷新既
	// 有行，不返回 500；不同主机的同名 PR 是不同键，天然并存。
	if err := s.pullRequestRepo.Upsert(link); err != nil {
		return nil, errs.ErrInternal
	}

	// 冲突路径下 link.ID 是新建值而非既有行主键，重新按唯一键读回规范行。
	stored, err := s.pullRequestRepo.FindByUnique(issue.ID, link.Provider, link.HostKey, link.RepoFullName, parsed.Number)
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

// SyncIssuePullRequests 重新拉取该 Issue 下全部已关联 PR 的最新状态。
// 只更新既有行，不新增、不删除；manual 与 synced 行都会被刷新。
// 每行是独立失败域：单条拉取/保存失败记入 failures 不中断其余行（旧数据保留），
// 客户端构造失败使该 repo 下每行各记一条；整体恒返回 200 形状的 {items, failures}。
//
// INT-175：三个阶段分离。访问面解析（凭证/集成读取，带缓存）串行；远端拉取
// 并发 pullRequestSyncConcurrency，随请求 ctx 断开；写回与响应组装串行且按
// 关联行原序，SQLite 单写连接下不再每行一次往返，响应顺序与旧串行实现一致。
// ctx 为请求 context（handler 传 c.Request.Context()），浏览器断开即停止后续
// 远端请求；测试传 nil 退化为 Background。30s 是整批兜底而非逐条预算。
func (s *IssueService) SyncIssuePullRequests(ctx context.Context, issueID, userID string) (*IssuePullRequestSyncResultResponse, error) {
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

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, pullRequestSyncTimeout)
	defer cancel()

	clients := make(map[string]githubapi.GitHubClient)
	clientKinds := make(map[string]pullRequestCredentialKind)
	clientErrs := make(map[string]error)
	// forgejo 行按「集成快照 id」缓存单次读取的访问面（客户端+主机快照，
	// 同一份集成行数据）；provider 分流见下方循环。
	forgeAccess := make(map[string]*forgejoSyncAccess)
	forgeAccessErrs := make(map[string]error)

	// 阶段 1（串行）：逐行解析访问面。构造结果按 repo/集成缓存，本阶段产
	// 生的失败直接计入 failures 且保持关联行顺序。
	fetchers := make([]func(context.Context, *model.IssuePullRequest) error, len(links))
	// 并发阶段各 goroutine 只写自己的下标：并行写不同下标的 slice 无竞争，
	// 不需要 mutex。
	failureAt := make([]IssuePullRequestSyncFailureItem, len(links))
	failed := make([]bool, len(links))
	markFailed := func(i int, item IssuePullRequestSyncFailureItem) {
		failureAt[i] = item
		failed[i] = true
	}
	for i := range links {
		link := &links[i]

		switch link.Provider {
		case model.IssuePullRequestProviderForgejo:
			if link.ForgeIntegrationID == "" {
				// 快照缺失是关联行自身的数据问题（非集成凭证面故障），
				// 文案要指引用户解除重联，不走统一脱敏文案。
				markFailed(i, pullRequestSyncFailure(link, "关联行缺少 Forgejo 集成快照，请解除后重新关联"))
				continue
			}
			// 一次读取集成行同时得到访问面与主机快照：主机不一致（集成改
			// 指别的主机）按单行失败拦下，取数不会用改指后的新基址。
			access, err := forgejoSyncAccessFor(s, forgeAccess, forgeAccessErrs, ctx, link)
			if err != nil {
				markFailed(i, pullRequestSyncFailure(link, s.forgejoSyncFailureText(err)))
				continue
			}
			if access.hostKey != link.HostKey {
				markFailed(i, pullRequestSyncFailure(link, "该 Forgejo 集成已改指其他主机，请解除后重新关联"))
				continue
			}
			reader := access.reader
			fetchers[i] = func(ctx context.Context, l *model.IssuePullRequest) error {
				pr, err := reader.GetPullRequest(ctx, l.RepoFullName, l.Number)
				if err != nil {
					return err
				}
				applyForgePullRequestData(l, pr, time.Now().UTC())
				return nil
			}
		case model.IssuePullRequestProviderGitHub:
			client, credKind, err := pullRequestSyncClient(s, clients, clientKinds, clientErrs, project, link)
			if err != nil {
				markFailed(i, pullRequestSyncFailure(link, err.Error()))
				continue
			}
			fetchers[i] = func(ctx context.Context, l *model.IssuePullRequest) error {
				pr, err := client.GetPullRequest(ctx, 0, l.RepoFullName, l.Number)
				if err != nil {
					return fmt.Errorf("拉取失败（%s）: %w%s", credKind.usage(), err, pullRequestAccessHint(err, credKind))
				}
				applyPullRequestData(l, pr, time.Now().UTC())
				return nil
			}
		default:
			// 未知 provider（历史脏行等）只算单行失败，不中断其余行。
			markFailed(i, pullRequestSyncFailure(link, fmt.Sprintf("不支持的 PR 来源 %q，请解除该关联后重新添加", link.Provider)))
			continue
		}
	}

	// 阶段 2（并发）：拉取远端 PR 详情。fetcher 自身把数据写进各自独占的
	// links[i]，无共享可变状态；ctx 断开时未开始的行不再发出新请求。
	workers := int64(pullRequestSyncConcurrency)
	var pending int
	for i := range links {
		if fetchers[i] != nil {
			pending++
		}
	}
	if pending > 0 {
		if n := int64(pending); n < workers {
			workers = n
		}
		sem := semaphore.NewWeighted(workers)
		var wg sync.WaitGroup
		for i := range links {
			fetch := fetchers[i]
			if fetch == nil {
				continue
			}
			link := &links[i]
			if err := sem.Acquire(ctx, 1); err != nil {
				// ctx 已取消：本行与后续所有行都不再发远端请求，统一记失败。
				for j := i; j < len(links); j++ {
					if fetchers[j] != nil && !failed[j] {
						markFailed(j, pullRequestSyncFailure(&links[j], "请求已取消或超时，未继续同步"))
					}
				}
				break
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer sem.Release(1)
				if err := fetch(ctx, link); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						markFailed(i, pullRequestSyncFailure(link, "请求已取消或超时，未继续同步"))
						return
					}
					markFailed(i, pullRequestSyncFailure(link, fetchErrText(s, link, err)))
				}
			}()
		}
		wg.Wait()
	}

	// 阶段 3（串行）：按原序逐行写回并组装响应——items/failures 的顺序与
	// 关联行创建顺序一致，与并发度无关。
	for i := range links {
		link := &links[i]
		if failed[i] {
			result.Failures = append(result.Failures, failureAt[i])
			continue
		}
		if fetchers[i] == nil {
			continue
		}
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

// fetchErrText 把并发拉取阶段的错误转成单行失败文案。GitHub 路径的错误已
// 在 fetcher 内包成带凭证来源与排查提示的文案，直接透出；其余（Forgejo 适
// 配器原文等）走统一脱敏口径。
func fetchErrText(s *IssueService, link *model.IssuePullRequest, err error) string {
	if link.Provider == model.IssuePullRequestProviderForgejo {
		return s.forgejoPullRequestFailureText("拉取 Forgejo PR", err)
	}
	return err.Error()
}

// pullRequestSyncClient 按 repo 复用/缓存客户端；构造错误同样按 repo 缓存，
// 让同 repo 的后续行各自记一条失败而不是重复构造。凭证来源随客户端一并返回
// 并缓存（构造失败时也缓存），供每行失败文案标明本次用的是哪种凭证。
func pullRequestSyncClient(s *IssueService, clients map[string]githubapi.GitHubClient, clientKinds map[string]pullRequestCredentialKind, clientErrs map[string]error, project *model.Project, link *model.IssuePullRequest) (githubapi.GitHubClient, pullRequestCredentialKind, error) {
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
	client, kind, err := s.pullRequestClient(project)
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

// pullRequestClient 为 attach/sync 构造 PR 读取客户端，并返回本次使用的凭证来源。
// 凭证选择集中在 credentials.pullRequestCredential：已配代码仓库 Token 只用它，
// 未配则匿名读公共 PR，不借公开仓库 Token；解密失败报错不降级。
func (s *IssueService) pullRequestClient(project *model.Project) (githubapi.GitHubClient, pullRequestCredentialKind, error) {
	tokenBytes, kind, appErr := s.credentials.pullRequestCredential(project)
	if appErr != nil {
		return nil, kind, appErr
	}
	return s.newPRClient(string(tokenBytes)), kind, nil
}

// pullRequestAccessHint 对 GitHub 的权限类错误、404 与限流追加排查提示。
// GitHub 对「PR 不存在」和「凭证无权访问该仓库」都返回 404，文案必须同时保留两种可能；
// 限流响应（HTTP 403/429、响应体含 rate limit 字样）与权限 403 无法从状态码区分，
// 且限流时 PR 与凭证都正常，必须给不同的提示，不能把用户引向查权限。
const pullRequestAccessHintText = "；PR 不存在或当前凭证无权访问该仓库，请确认仓库访问范围与 Pull requests 读权限"
const pullRequestRateLimitHintText = "；GitHub API 限流，请稍后重试"
const pullRequestRateLimitAnonymousHintText = "（匿名访问限 60 次/小时，配置 Token 可提高限额）"

func pullRequestAccessHint(err error, kind pullRequestCredentialKind) string {
	var statusErr *githubapi.StatusError
	if !errors.As(err, &statusErr) {
		return ""
	}
	switch statusErr.StatusCode {
	case 403, 429:
		body := strings.ToLower(statusErr.Body)
		if strings.Contains(body, "rate limit") || strings.Contains(body, "abuse detection") {
			// 次级限流对已配置 Token 的请求同样触发，限额说明只在匿名时适用。
			if kind == pullRequestCredentialAnonymous {
				return pullRequestRateLimitHintText + pullRequestRateLimitAnonymousHintText
			}
			return pullRequestRateLimitHintText
		}
		if statusErr.StatusCode == 403 {
			return pullRequestAccessHintText
		}
		return ""
	case 401, 404:
		return pullRequestAccessHintText
	default:
		return ""
	}
}

// forgejoPullRequestFailureText 把 Forgejo 取数失败转成可回显的文案（attach
// 的错误信息与 sync 的 failures[].error 都走这里）：状态码与排查提示保留，
// 适配器错误原文可能带集成配置的 API 绝对地址（含 allow_private_network 的
// 内网基址）与响应体，只进服务端日志不外显给项目成员。
func (s *IssueService) forgejoPullRequestFailureText(op string, err error) string {
	var statusErr *forge.StatusError
	if errors.As(err, &statusErr) {
		// 状态码分支同样落日志：URL 与响应体是排查 404/403/限流的关键痕迹，
		// 只进日志不外显。
		s.logger.Error(op+"失败", zap.Int("status", statusErr.StatusCode), zap.String("url", statusErr.URL), zap.String("body", statusErr.Body))
		return fmt.Sprintf("%s 失败（Forgejo API 状态码 %d）%s", op, statusErr.StatusCode, forgejoPullRequestAccessHint(err))
	}
	// 传输层错误可能带内部主机名，响应解析失败可能意味着端点异常——原文
	// 进日志，对外归一为连接/响应异常，不把排查引向单一方面。
	s.logger.Error(op+"失败", zap.Error(err))
	return op + " 失败：Forgejo 服务连接或响应异常，请检查集成配置与网络"
}

// forgejoPullRequestAccessHint 对 Forgejo 的权限类错误、404 与限流追加排查
// 提示（口径对齐 pullRequestAccessHint）：Forgejo 对「PR 不存在」和「机器人
// PAT 无权访问该仓库」都返回 404，文案必须同时保留两种可能；限流响应无法与
// 权限 403 区分且处置不同，按 RateLimited 预判定给不同提示。
const forgejoPullRequestAccessHintText = "；PR 不存在或集成机器人 PAT 无权访问该仓库，请确认 Forgejo 集成的仓库授权范围"
const forgejoPullRequestRateLimitHintText = "；Forgejo API 限流，请稍后重试"

func forgejoPullRequestAccessHint(err error) string {
	var statusErr *forge.StatusError
	if !errors.As(err, &statusErr) {
		return ""
	}
	if statusErr.RateLimited || statusErr.StatusCode == 429 {
		return forgejoPullRequestRateLimitHintText
	}
	switch statusErr.StatusCode {
	case 401, 403, 404:
		return forgejoPullRequestAccessHintText
	default:
		return ""
	}
}

// forgejoIntegrationForProject 解析 attach 可用的 Forgejo 集成，返回集成行
// 本身（供 ForgejoClientFor 单次读取，不二次按 id 重读）：项目绑定的代码集
// 成必须在 PR URL 主机上——host_key 在同 kind 存活行内有部分唯一索引，绑定
// 行主机一致即它就是该主机的集成。部署级机器人 PAT 只服务代码仓真的在这个
// Forgejo 上的项目，其它项目不得借它读项目外仓库的 PR 元数据（owner/repo
// 仅是 URL 文本，不参与授权判定）。同主机内跨仓库是有意支持的能力（跨仓
// PR 关联）。集成被项目引用时不可删除（DeleteIfUnreferenced），项目绑定指
// 向的行恒存在。
func (s *IssueService) forgejoIntegrationForProject(project *model.Project, hostKey string) (*model.ForgeIntegration, error) {
	if hostKey == "" {
		return nil, errs.ErrForgeIntegrationNotConfigured
	}
	if project.CodeIntegrationID == "" {
		// parseRepoBinding 对 GitHub 仓恒返回空集成 id：空绑定不代表没配代码仓，
		// 很可能是代码仓在内置 GitHub 上（含只配公开仓、未单独配代码仓的回落
		// 情形，CodeRepo() 同口径判定）——文案按事实给路径，别指向死路。
		if owner, _ := project.CodeRepo(); owner != "" {
			return nil, errs.New(errs.ErrForgeIntegrationNotConfigured.Code,
				"该项目的代码仓在 GitHub 上，仅支持关联 GitHub PR；要关联 Forgejo PR 请先把项目代码仓改配到对应 Forgejo 实例")
		}
		return nil, errs.New(errs.ErrForgeIntegrationNotConfigured.Code,
			"该项目未配置代码仓库，请先在项目设置里配置代码仓库后再关联 PR")
	}
	bound, err := s.forgeIntegrationRepo.FindByID(project.CodeIntegrationID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.New(errs.ErrForgeIntegrationNotConfigured.Code, "该项目绑定的代码集成已不存在，请先在项目设置里重新配置代码仓库")
		}
		return nil, errs.ErrInternal
	}
	if bound.Kind != model.ForgeIntegrationKindForgejo {
		return nil, errs.New(errs.ErrForgeIntegrationNotConfigured.Code, "该项目绑定的代码集成类型不是 Forgejo，仅支持关联 Forgejo PR；请在项目设置里改配 Forgejo 集成")
	}
	if forges.HostKeyNoScheme(bound.BaseURL) != hostKey {
		return nil, errs.New(errs.ErrForgeIntegrationNotConfigured.Code, "该项目绑定的代码集成与 PR 链接协议/端口不一致，请在项目设置里调整代码仓库后再关联 PR")
	}
	return bound, nil
}

// forgejoSyncAccess 一次读取集成行得到的同步访问面：host 校验与 PR 取数用
// 同一份集成行数据——不存在「校验用旧行、取数用新行」的竞态窗口（R8）。
type forgejoSyncAccess struct {
	reader  forgePRReader
	hostKey string
}

// forgejoSyncAccessFor 按「集成快照 id」复用/缓存访问面；构造错误同样按 id
// 缓存，同集成的后续行各自记一条失败而不是重复构造。集成行已删的行由上游
// 凭证面文案统一处置。
func forgejoSyncAccessFor(s *IssueService, entries map[string]*forgejoSyncAccess, entryErrs map[string]error, ctx context.Context, link *model.IssuePullRequest) (*forgejoSyncAccess, error) {
	if entry, ok := entries[link.ForgeIntegrationID]; ok {
		return entry, nil
	}
	if err, failed := entryErrs[link.ForgeIntegrationID]; failed {
		return nil, err
	}
	row, err := s.forgeIntegrationRepo.FindByID(link.ForgeIntegrationID)
	if err != nil {
		err = classifyForgeIntegrationReadErr(err)
		entryErrs[link.ForgeIntegrationID] = err
		return nil, err
	}
	if s.forgeSource == nil {
		return nil, errors.New("Forgejo 集成凭证来源未配置")
	}
	httpClient, apiBase, token, err := s.forgeSource.ForgejoClientFor(ctx, row)
	if err != nil {
		err = classifyForgeIntegrationReadErr(err)
		entryErrs[link.ForgeIntegrationID] = err
		return nil, err
	}
	entry := &forgejoSyncAccess{reader: s.newForgejoPRReader(httpClient, apiBase, token), hostKey: forges.HostKeyNoScheme(row.BaseURL)}
	entries[link.ForgeIntegrationID] = entry
	return entry, nil
}

// classifyForgeIntegrationReadErr 集成读取错误三类：行已删（NotFound 或 kind
// 不符的哨兵）给「不可用或已被删除」；已有业务文案的错误（如 PAT 解密失败）
// 原样透出；其余（锁忙/IO 等瞬时故障）给中性提示——瞬时故障不能引导用户去
// 删集成重配。
func classifyForgeIntegrationReadErr(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("Forgejo 集成不可用或已被删除，请检查集成管理配置")
	}
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return errors.New(appErr.Message)
	}
	return errors.New("读取 Forgejo 集成失败，请稍后重试")
}

// forgejoSyncFailureText 记日志并回传已分类的错误文案（forgejoSyncAccessFor
// 返回的 err 已按受众分好类）。
func (s *IssueService) forgejoSyncFailureText(err error) string {
	s.logger.Error("Forgejo 关联行同步失败", zap.Error(err))
	return err.Error()
}

// parsePullRequestLink 分流解析 PR 链接（INT-181）：github.com 域 + /pull/{n}
// 走 GitHub 既有路径；其他主机 + /{owner}/{repo}/pulls/{n} 走 Forgejo 路径并
// 带出主机的归一键（forges.HostKey，与 forge_integrations.host_key 同口径）。
// github.com 域内形状不符（如 /pulls/{n}）按无效处理，不误判成 Forgejo；其余
// 形状一律无效，由调用方报 40001 并说明两种支持的格式。两种路径都容忍 http、
// www 前缀（仅 GitHub）与 /files、query、fragment 等尾部。
func parsePullRequestLink(raw string) (pullRequestLink, bool) {
	trimmed := strings.TrimSpace(raw)
	if m := pullRequestURLPattern.FindStringSubmatch(trimmed); m != nil {
		owner, repo, number, ok := githubPullRequestMatch(m)
		if !ok {
			return pullRequestLink{}, false
		}
		return pullRequestLink{Provider: model.IssuePullRequestProviderGitHub, Owner: owner, Repo: repo, Number: number}, true
	}
	if m := forgejoPullRequestURLPattern.FindStringSubmatch(trimmed); m != nil {
		scheme := strings.ToLower(m[1])
		u, err := url.Parse(scheme + "://" + m[2])
		if err != nil || u.Hostname() == "" {
			return pullRequestLink{}, false
		}
		// github.com/www.github.com 域（与 forges.HostKey 同口径的归一比较：
		// 尾点、大小写归一后同域即拒绝；Hostname 已剥端口，显式端口的主机名
		// 仍命中）留给 GitHub 路径：形状不符就是无效，不落进 Forgejo 分支
		// （也避免把 GitHub PR 误挂到 Forgejo 集成上）。
		host := strings.ToLower(strings.TrimRight(u.Hostname(), "."))
		if host == "github.com" || host == "www.github.com" {
			return pullRequestLink{}, false
		}
		if !isGitHubPathName(m[3]) || !isGitHubPathName(m[4]) {
			return pullRequestLink{}, false
		}
		number, err := strconv.Atoi(m[5])
		if err != nil || number <= 0 {
			return pullRequestLink{}, false
		}
		// HostKeyNoScheme 解析不出主机键（如 host 全是点段）返回空串：空键
		// 不参与集成匹配（脏行 host_key 也是空串），按无效链接处理。PR 链
		// 接匹配用不含 scheme 的口径——同一端点的 http/https 拼写归一同键。
		hostKey := forges.HostKeyNoScheme(scheme + "://" + m[2])
		if hostKey == "" {
			return pullRequestLink{}, false
		}
		return pullRequestLink{
			Provider: model.IssuePullRequestProviderForgejo,
			Owner:    m[3],
			Repo:     m[4],
			Number:   number,
			HostKey:  hostKey,
		}, true
	}
	return pullRequestLink{}, false
}

// githubPullRequestMatch 校验 GitHub 正则捕获组：排除纯点段与点开头结尾的
// owner/repo（见 isGitHubPathName），编号须为正整数。
func githubPullRequestMatch(m []string) (owner, repo string, number int, ok bool) {
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
func applyPullRequestData(link *model.IssuePullRequest, pr *githubapi.PullRequest, now time.Time) {
	if pr.RepoFullName != "" {
		link.RepoFullName = pr.RepoFullName
	}
	link.Title = pr.Title
	if pr.HTMLURL != "" {
		link.HTMLURL = pr.HTMLURL
	}
	link.State = pullRequestStateFromGitHub(pr)
	link.IsDraft = pr.Draft
	link.AuthorLogin = pr.AuthorLogin
	link.HeadRef = pr.HeadRef
	link.BaseRef = pr.BaseRef
	link.MergedAt = nil
	if pr.MergedAt != nil {
		mergedAt := pr.MergedAt.UTC()
		link.MergedAt = &mergedAt
	}
	link.ClosedAt = nil
	if pr.ClosedAt != nil {
		closedAt := pr.ClosedAt.UTC()
		link.ClosedAt = &closedAt
	}
	link.SyncedAt = now
	link.UpdatedAt = now
}

// pullRequestStateFromGitHub 映射 PR 状态：GitHub closed 且 merged_at 非空视为 merged。
func pullRequestStateFromGitHub(pr *githubapi.PullRequest) model.IssuePullRequestState {
	if pr.State != "closed" {
		return model.IssuePullRequestStateOpen
	}
	if pr.MergedAt != nil {
		return model.IssuePullRequestStateMerged
	}
	return model.IssuePullRequestStateClosed
}

// applyForgePullRequestData 把 Forgejo PR 详情刷进关联行，字段口径与
// applyPullRequestData 一致（状态与合并时间一并重置，closed→open 复活也能纠
// 正回来）：repo_full_name 归一到平台返回的 canonical 名（base 仓库 full_name），
// 状态按 State/Merged 映射。集成快照不在此重写——sync 按关联行已有的
// ForgeIntegrationID 重建客户端。
func applyForgePullRequestData(link *model.IssuePullRequest, pr *forge.PullRequest, now time.Time) {
	if pr.RepoFullName != "" {
		link.RepoFullName = pr.RepoFullName
	}
	link.Title = pr.Title
	if pr.HTMLURL != "" {
		link.HTMLURL = pr.HTMLURL
	}
	link.State = pullRequestStateFromForge(pr)
	link.IsDraft = pr.Draft
	link.AuthorLogin = pr.AuthorLogin
	link.HeadRef = pr.HeadRef
	link.BaseRef = pr.BaseRef
	link.MergedAt = nil
	if pr.MergedAt != nil {
		mergedAt := pr.MergedAt.UTC()
		link.MergedAt = &mergedAt
	}
	link.ClosedAt = nil
	if pr.ClosedAt != nil {
		closedAt := pr.ClosedAt.UTC()
		link.ClosedAt = &closedAt
	}
	link.SyncedAt = now
	link.UpdatedAt = now
}

// pullRequestStateFromForge 映射 PR 状态：Forgejo 的 closed 且平台标记合并
// （merged=true 或 merged_at 非空，历史/异常快照两者可能缺一）视为 merged——
// 与 pullRequestStateFromGitHub 的 closed+merged_at 口径对齐，避免「平台已标
// merged 但 merged_at 缺失」的老数据永久显示 closed。
func pullRequestStateFromForge(pr *forge.PullRequest) model.IssuePullRequestState {
	if pr.State != "closed" {
		return model.IssuePullRequestStateOpen
	}
	if pr.Merged || pr.MergedAt != nil {
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
