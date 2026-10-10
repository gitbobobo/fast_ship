package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/repository"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// issueUserPayload / issueMilestonePayload 是 issue_github_meta 表
// assignees_json / milestone_json 列的存储格式，读路径按同一结构解析。
type issueUserPayload struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type issueMilestonePayload struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Description string `json:"description"`
}

// issueMirror 负责 GitHub→本地的镜像同步：先拉全远端分页再写库、同一项目用
// 内存锁防并发。push 路径（创建/更新 issue、发评论）经 upsertGitHubIssue、
// syncTimeline、recordPushedComment 把远端响应写回本地，不经同步锁。
// 依赖在构造时按值固化，只有 newClient 由 IssueService 闭包委托、调用时现取，
// 保证测试替换 s.newClient 对镜像同样生效；其余字段测试不会在构造后更换。
type issueMirror struct {
	issueRepo           *repository.IssueRepository
	gitHubMetaRepo      *repository.IssueGitHubMetaRepository
	commentRepo         *repository.IssueCommentRepository
	timelineRepo        *repository.IssueTimelineRepository
	recRepo             *repository.IssueRecommendationRepository
	syncStateRepo       *repository.IssueSyncStateRepository
	githubRepoLabelRepo *repository.GitHubRepoLabelRepository
	projectRepo         *repository.ProjectRepository
	cfg                 *config.Config
	logger              *zap.Logger
	newClient           gitHubIssueClientFactory
	mu                  sync.Mutex
	syncing             map[string]struct{}
}

func (m *issueMirror) Sync(ctx context.Context, project *model.Project) (*IssueSyncResponse, error) {
	if !m.beginSync(project.ID) {
		return nil, errs.ErrIssueSyncRunning
	}
	defer m.endSync(project.ID)

	state, err := m.syncStateRepo.GetOrCreate(project.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	startedAt := time.Now().UTC()
	state.Status = model.IssueSyncStatusRunning
	state.LastError = ""
	state.LastSyncedAt = &startedAt
	if err := m.syncStateRepo.Save(state); err != nil {
		return nil, errs.ErrInternal
	}

	failSync := func(syncErr error) (*IssueSyncResponse, error) {
		failedAt := time.Now().UTC()
		state.Status = model.IssueSyncStatusFailed
		state.LastSyncedAt = &failedAt
		state.LastError = syncErr.Error()
		_ = m.syncStateRepo.Save(state)
		return nil, syncErr
	}

	tokenBytes, appErr := requiredProjectGitHubToken(project, m.cfg, m.logger)
	if appErr != nil {
		return failSync(appErr)
	}

	client := m.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
	if err := client.ValidateRepository(ctx); err != nil {
		return failSync(errs.New(errs.ErrGitHubAPI.Code, "无法访问 GitHub 仓库或 Token 无效"))
	}

	if err := m.syncRepositoryLabels(ctx, client, project.ID); err != nil {
		m.logger.Warn("同步仓库标签失败", zap.String("project_id", project.ID), zap.Error(err))
	}

	var since *time.Time
	if state.LastIssueUpdatedAt != nil {
		t := state.LastIssueUpdatedAt.Add(-1 * time.Second)
		since = &t
	}

	const perPage = 100
	var (
		page              = 1
		syncedIssues      int
		syncedComments    int
		syncedTimeline    int
		maxIssueUpdatedAt *time.Time
	)

	for {
		items, resp, err := client.ListIssues(ctx, "all", since, page, perPage)
		if err != nil {
			return failSync(errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("同步 GitHub Issues 失败: %v", err)))
		}

		for _, item := range items {
			if item == nil || item.IsPullRequest() || item.GetID() == 0 {
				continue
			}

			issue, syncErr := m.upsertGitHubIssue(project.ID, item)
			if syncErr != nil {
				return failSync(syncErr)
			}

			commentCount, syncErr := m.syncComments(ctx, client, issue, item.GetNumber())
			if syncErr != nil {
				return failSync(syncErr)
			}
			timelineCount, syncErr := m.syncTimeline(ctx, client, issue, item.GetNumber())
			if syncErr != nil {
				return failSync(syncErr)
			}

			syncedIssues++
			syncedComments += commentCount
			syncedTimeline += timelineCount

			updatedAt := item.GetUpdatedAt().UTC()
			if maxIssueUpdatedAt == nil || updatedAt.After(*maxIssueUpdatedAt) {
				maxIssueUpdatedAt = &updatedAt
			}
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	completedAt := time.Now().UTC()
	state.Status = model.IssueSyncStatusCompleted
	state.LastSyncedAt = &completedAt
	state.LastSuccessfulSyncAt = &completedAt
	state.LastError = ""
	if maxIssueUpdatedAt != nil {
		state.LastIssueUpdatedAt = maxIssueUpdatedAt
	}
	if err := m.syncStateRepo.Save(state); err != nil {
		return nil, errs.ErrInternal
	}

	resp := &IssueSyncResponse{
		ProjectId:           project.ID,
		SyncedIssueCount:    syncedIssues,
		SyncedCommentCount:  syncedComments,
		SyncedTimelineCount: syncedTimeline,
		StartedAt:           formatTime(startedAt),
		CompletedAt:         formatTime(completedAt),
	}
	if state.LastIssueUpdatedAt != nil {
		value := formatTime(state.LastIssueUpdatedAt.UTC())
		resp.LastIssueUpdatedAt = &value
	}
	return resp, nil
}

func (m *issueMirror) SyncAll(ctx context.Context) {
	projects, err := m.projectRepo.ListAll()
	if err != nil {
		m.logger.Error("list projects for issue sync failed", zap.Error(err))
		return
	}

	for i := range projects {
		project := projects[i]
		if ctx.Err() != nil {
			return
		}
		if !project.IsGitHubConfigured() {
			continue
		}
		if _, err := m.Sync(ctx, &project); err != nil {
			m.logger.Warn("background issue sync failed", zap.String("project_id", project.ID), zap.Error(err))
		}
	}
}

func (m *issueMirror) syncRepositoryLabels(ctx context.Context, client gitHubIssueClient, projectID string) error {
	const perPage = 100
	page := 1
	now := time.Now().UTC()
	var allLabels []model.GitHubRepoLabel

	for {
		items, resp, err := client.ListRepositoryLabels(ctx, page, perPage)
		if err != nil {
			return err
		}

		for _, item := range items {
			if item == nil {
				continue
			}
			name := strings.TrimSpace(item.GetName())
			if name == "" {
				continue
			}
			allLabels = append(allLabels, model.GitHubRepoLabel{
				ProjectID:   projectID,
				Name:        name,
				Color:       item.GetColor(),
				Description: item.GetDescription(),
				SyncedAt:    now,
			})
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	if err := m.githubRepoLabelRepo.ReplaceAllForProject(projectID, allLabels); err != nil {
		return err
	}
	return nil
}

func (m *issueMirror) upsertGitHubIssue(projectID string, item *ghclient.Issue) (*model.Issue, error) {
	now := time.Now().UTC()

	var issue *model.Issue
	meta, err := m.gitHubMetaRepo.FindByProjectAndGitHubID(projectID, item.GetID())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	if meta != nil {
		issue, err = m.issueRepo.FindByID(meta.IssueID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
	}

	if issue == nil {
		sequenceNumber, err := m.issueRepo.NextSequenceNumber(projectID)
		if err != nil {
			return nil, errs.ErrInternal
		}
		issue = &model.Issue{
			ID:             uuid.NewString(),
			ProjectID:      projectID,
			Source:         model.IssueSourceGitHub,
			SequenceNumber: sequenceNumber,
		}
	}

	issue.Source = model.IssueSourceGitHub
	issue.State = model.IssueState(item.GetState())
	issue.StateReason = item.GetStateReason()
	issue.Title = item.GetTitle()
	issue.Body = item.GetBody()
	issue.BodyHTML = item.GetBodyHTML()
	issue.AuthorUserID = ""
	issue.AuthorLogin = item.GetUser().GetLogin()
	issue.AuthorAvatarURL = item.GetUser().GetAvatarURL()
	issue.CreatedAt = item.GetCreatedAt().UTC()
	issue.UpdatedAt = item.GetUpdatedAt().UTC()
	if closedAt := item.GetClosedAt(); !closedAt.IsZero() {
		value := closedAt.UTC()
		issue.ClosedAt = &value
	} else {
		issue.ClosedAt = nil
	}

	persistIssue := func(tx *gorm.DB) error {
		if meta == nil {
			return m.issueRepo.CreateTx(tx, issue)
		}
		return m.issueRepo.SaveTx(tx, issue)
	}

	if issue.State == model.IssueStateClosed {
		// GitHub 上已关闭的 issue 不可再被推荐；落库与删推荐同一事务，Delete 失败时回滚避免残留
		if err := m.issueRepo.Transaction(func(tx *gorm.DB) error {
			if err := persistIssue(tx); err != nil {
				return err
			}
			return m.recRepo.DeleteTx(tx, issue.ID)
		}); err != nil {
			return nil, errs.ErrInternal
		}
	} else if err := persistIssue(m.issueRepo.DB()); err != nil {
		return nil, errs.ErrInternal
	}

	gitHubMeta := &model.IssueGitHubMeta{
		IssueID:           issue.ID,
		ProjectID:         projectID,
		GitHubIssueID:     item.GetID(),
		GitHubNodeID:      item.GetNodeID(),
		Number:            item.GetNumber(),
		HTMLURL:           item.GetHTMLURL(),
		AuthorAssociation: item.GetAuthorAssociation(),
		AssigneesJSON:     toJSONString(mapUsers(item.Assignees)),
		LabelsJSON:        toJSONString(mapLabelNames(item.Labels)),
		MilestoneJSON:     toJSONString(mapMilestone(item.Milestone)),
		ReactionsJSON:     toJSONString(mapReactions(item.Reactions)),
		CommentsCount:     item.GetComments(),
		Locked:            item.GetLocked(),
		ActiveLockReason:  item.GetActiveLockReason(),
		SyncedAt:          now,
		RawJSON:           toJSONString(item),
	}

	if err := m.gitHubMetaRepo.Upsert(gitHubMeta); err != nil {
		return nil, errs.ErrInternal
	}

	stored, err := m.issueRepo.FindByID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return stored, nil
}

// syncComments 先拉完该 Issue 的全部评论分页并映射成 model，再由 ReplaceSynced
// 单事务写入；任一分页失败时本地集合保持同步前数据，一个字也不写。
func (m *issueMirror) syncComments(ctx context.Context, client gitHubIssueClient, issue *model.Issue, issueNumber int) (int, error) {
	const perPage = 100
	page := 1
	var comments []*model.IssueComment

	for {
		items, resp, err := client.ListIssueComments(ctx, issueNumber, page, perPage)
		if err != nil {
			return 0, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("同步 Issue 评论失败: %v", err))
		}

		for _, item := range items {
			if item == nil || item.GetID() == 0 {
				continue
			}
			comments = append(comments, buildGitHubIssueCommentModel(issue.ID, item))
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	if err := m.commentRepo.ReplaceSynced(issue.ID, comments); err != nil {
		return 0, errs.ErrInternal
	}
	return len(comments), nil
}

// syncTimeline 先拉完该 Issue 的全部时间线分页并映射成 model，再由 ReplaceSynced
// 单事务写入；任一分页失败时本地集合保持同步前数据，一个字也不写。
func (m *issueMirror) syncTimeline(ctx context.Context, client gitHubIssueClient, issue *model.Issue, issueNumber int) (int, error) {
	const perPage = 100
	page := 1
	var events []*model.IssueTimelineEvent

	for {
		items, resp, err := client.ListIssueTimeline(ctx, issueNumber, page, perPage)
		if err != nil {
			return 0, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("同步 Issue 动态失败: %v", err))
		}

		for _, item := range items {
			if item == nil {
				continue
			}
			events = append(events, &model.IssueTimelineEvent{
				ID:              uuid.NewString(),
				IssueID:         issue.ID,
				EventKey:        buildTimelineEventKey(item),
				GitHubEventID:   item.GetID(),
				EventType:       item.GetEvent(),
				ActorLogin:      firstNonEmpty(item.GetActor().GetLogin(), item.GetUser().GetLogin()),
				ActorAvatarURL:  firstNonEmpty(item.GetActor().GetAvatarURL(), item.GetUser().GetAvatarURL()),
				Body:            item.GetBody(),
				Summary:         summarizeTimeline(item),
				PayloadJSON:     toJSONString(item),
				GitHubCreatedAt: item.GetCreatedAt().UTC(),
			})
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	if err := m.timelineRepo.ReplaceSynced(issue.ID, events); err != nil {
		return 0, errs.ErrInternal
	}
	return len(events), nil
}

// recordPushedComment 把刚推送到 GitHub 的评论写回本地：upsert 评论行、
// 推进 issue.UpdatedAt、累计 meta.CommentsCount。调用方在成功后自行推进
// 已读水位、记日志；写回不经同步锁。
func (m *issueMirror) recordPushedComment(issue *model.Issue, comment *model.IssueComment) error {
	if err := m.commentRepo.Upsert(comment); err != nil {
		return errs.ErrInternal
	}
	now := time.Now().UTC()
	updatedAt := comment.GitHubUpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	issue.UpdatedAt = updatedAt
	if err := m.issueRepo.Save(issue); err != nil {
		return errs.ErrInternal
	}
	meta := issue.GitHubMeta
	meta.CommentsCount++
	meta.SyncedAt = now
	if err := m.gitHubMetaRepo.Upsert(meta); err != nil {
		return errs.ErrInternal
	}
	return nil
}

func (m *issueMirror) beginSync(projectID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.syncing[projectID]; exists {
		return false
	}
	m.syncing[projectID] = struct{}{}
	return true
}

func (m *issueMirror) endSync(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.syncing, projectID)
}

func buildGitHubIssueCommentModel(issueID string, item *ghclient.IssueComment) *model.IssueComment {
	comment := &model.IssueComment{
		ID:                uuid.NewString(),
		IssueID:           issueID,
		Source:            model.IssueSourceGitHub,
		GitHubCommentID:   item.GetID(),
		GitHubNodeID:      item.GetNodeID(),
		Body:              item.GetBody(),
		BodyHTML:          item.GetBodyHTML(),
		HTMLURL:           item.GetHTMLURL(),
		AuthorLogin:       item.GetUser().GetLogin(),
		AuthorAvatarURL:   item.GetUser().GetAvatarURL(),
		AuthorAssociation: item.GetAuthorAssociation(),
		ReactionsJSON:     toJSONString(mapReactions(item.Reactions)),
		RawJSON:           toJSONString(item),
	}
	if createdAt := item.GetCreatedAt(); !createdAt.IsZero() {
		comment.GitHubCreatedAt = createdAt.UTC()
	}
	if updatedAt := item.GetUpdatedAt(); !updatedAt.IsZero() {
		comment.GitHubUpdatedAt = updatedAt.UTC()
	}
	if comment.GitHubCreatedAt.IsZero() {
		comment.GitHubCreatedAt = time.Now().UTC()
	}
	if comment.GitHubUpdatedAt.IsZero() {
		comment.GitHubUpdatedAt = comment.GitHubCreatedAt
	}
	return comment
}

func mapUsers(users []*gh.User) []issueUserPayload {
	out := make([]issueUserPayload, 0, len(users))
	for _, user := range users {
		if user == nil || user.GetLogin() == "" {
			continue
		}
		out = append(out, issueUserPayload{
			Login:     user.GetLogin(),
			AvatarURL: user.GetAvatarURL(),
		})
	}
	return out
}

func mapLabelNames(labels []*gh.Label) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if label == nil {
			continue
		}
		name := strings.TrimSpace(label.GetName())
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func mapMilestone(m *gh.Milestone) *issueMilestonePayload {
	if m == nil || m.GetTitle() == "" {
		return nil
	}
	return &issueMilestonePayload{
		Number:      m.GetNumber(),
		Title:       m.GetTitle(),
		State:       m.GetState(),
		Description: m.GetDescription(),
	}
}

func mapReactions(r *gh.Reactions) IssueReactionSummaryResponse {
	if r == nil {
		return IssueReactionSummaryResponse{}
	}
	return IssueReactionSummaryResponse{
		TotalCount: r.GetTotalCount(),
		Plus1:      r.GetPlusOne(),
		Minus1:     r.GetMinusOne(),
		Laugh:      r.GetLaugh(),
		Hooray:     r.GetHooray(),
		Confused:   r.GetConfused(),
		Heart:      r.GetHeart(),
		Rocket:     r.GetRocket(),
		Eyes:       r.GetEyes(),
	}
}

func summarizeTimeline(item *ghclient.TimelineEvent) string {
	eventType := item.GetEvent()
	switch eventType {
	case "labeled":
		return fmt.Sprintf("添加了标签 %s", item.GetLabel().GetName())
	case "unlabeled":
		return fmt.Sprintf("移除了标签 %s", item.GetLabel().GetName())
	case "assigned":
		return fmt.Sprintf("指派给 %s", item.GetAssignee().GetLogin())
	case "unassigned":
		return fmt.Sprintf("取消指派 %s", item.GetAssignee().GetLogin())
	case "milestoned":
		return fmt.Sprintf("加入里程碑 %s", item.GetMilestone().GetTitle())
	case "demilestoned":
		return fmt.Sprintf("移出里程碑 %s", item.GetMilestone().GetTitle())
	case "renamed":
		return fmt.Sprintf("标题从 %s 改为 %s", item.GetRename().GetFrom(), item.GetRename().GetTo())
	case "closed":
		return "关闭了问题"
	case "reopened":
		return "重新打开了问题"
	case "locked":
		return "锁定了讨论"
	case "unlocked":
		return "解锁了讨论"
	case "cross-referenced":
		source := item.GetSource()
		if source != nil && source.Issue != nil {
			return fmt.Sprintf("被 #%d 交叉引用", source.Issue.GetNumber())
		}
		return "发生了交叉引用"
	case "referenced":
		if item.GetCommitID() != "" {
			return fmt.Sprintf("被提交 %s 引用", shortSHA(item.GetCommitID()))
		}
		return "被提交引用"
	case "commented":
		return "添加了评论"
	case "subscribed":
		return "订阅了此问题"
	case "unsubscribed":
		return "取消订阅此问题"
	case "added_type", "issue_type_added":
		if item.GetIssueType() != nil {
			return fmt.Sprintf("添加了问题类型 %s", item.GetIssueType().GetName())
		}
		return "添加了问题类型"
	case "removed_type", "issue_type_removed":
		if item.GetIssueType() != nil {
			return fmt.Sprintf("移除了问题类型 %s", item.GetIssueType().GetName())
		}
		return "移除了问题类型"
	default:
		if eventType == "" {
			return "发生了更新"
		}
		return eventType
	}
}

func buildTimelineEventKey(item *ghclient.TimelineEvent) string {
	if item.GetID() != 0 {
		return fmt.Sprintf("gh:%d", item.GetID())
	}

	parts := []string{
		item.GetEvent(),
		formatTime(item.GetCreatedAt().UTC()),
		firstNonEmpty(item.GetActor().GetLogin(), item.GetUser().GetLogin()),
		item.GetLabel().GetName(),
		item.GetMilestone().GetTitle(),
		item.GetCommitID(),
		item.GetBody(),
	}
	return "fallback:" + strings.Join(parts, "|")
}

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func (s *IssueService) SyncProjectIssues(projectID, userID string) (*IssueSyncResponse, error) {
	project, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	if !project.IsGitHubConfigured() {
		return nil, errs.ErrProjectGitHubNotConfigured
	}

	return s.mirror.Sync(context.Background(), project)
}

func (s *IssueService) SyncAllProjectsIncremental(ctx context.Context) {
	s.mirror.SyncAll(ctx)
}
