package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
)

func testGitHubSyncIssue(id int64, number int, at time.Time) *ghclient.Issue {
	return &ghclient.Issue{
		Issue: gh.Issue{
			ID:        int64Ptr(id),
			NodeID:    stringPtr(fmt.Sprintf("I_kw%d", id)),
			Number:    intPtr(number),
			State:     stringPtr("open"),
			Title:     stringPtr(fmt.Sprintf("Issue %d", number)),
			Body:      stringPtr("body"),
			HTMLURL:   stringPtr(fmt.Sprintf("https://github.com/owner/repo/issues/%d", number)),
			User:      &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
			CreatedAt: &gh.Timestamp{Time: at},
			UpdatedAt: &gh.Timestamp{Time: at},
		},
	}
}

func testGitHubSyncComment(id int64, body string, at time.Time) *ghclient.IssueComment {
	return &ghclient.IssueComment{
		IssueComment: gh.IssueComment{
			ID:        int64Ptr(id),
			NodeID:    stringPtr(fmt.Sprintf("IC_kw%d", id)),
			Body:      stringPtr(body),
			HTMLURL:   stringPtr(fmt.Sprintf("https://github.com/owner/repo/issues/42#issuecomment-%d", id)),
			User:      &gh.User{Login: stringPtr("bob"), AvatarURL: stringPtr("https://example.com/bob.png")},
			CreatedAt: &gh.Timestamp{Time: at},
			UpdatedAt: &gh.Timestamp{Time: at},
		},
	}
}

func testGitHubSyncTimelineEvent(id int64, event string, at time.Time) *ghclient.TimelineEvent {
	return &ghclient.TimelineEvent{
		Timeline: gh.Timeline{
			ID:        int64Ptr(id),
			Event:     stringPtr(event),
			Actor:     &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
			CreatedAt: &gh.Timestamp{Time: at},
		},
	}
}

func syncedGitHubIssueID(t *testing.T, svc *testServices, projectID string) string {
	t.Helper()
	var issue model.Issue
	if err := svc.db.First(&issue, "project_id = ?", projectID).Error; err != nil {
		t.Fatalf("load synced issue: %v", err)
	}
	return issue.ID
}

func storedCommentsByGitHubID(t *testing.T, svc *testServices, issueID string) map[int64]model.IssueComment {
	t.Helper()
	comments, err := svc.commentRepo.ListAllByIssueID(issueID)
	if err != nil {
		t.Fatalf("list stored comments: %v", err)
	}
	result := make(map[int64]model.IssueComment, len(comments))
	for _, c := range comments {
		result[c.GitHubCommentID] = c
	}
	return result
}

func storedTimelineByKey(t *testing.T, svc *testServices, issueID string) map[string]model.IssueTimelineEvent {
	t.Helper()
	var events []model.IssueTimelineEvent
	if err := svc.db.Where("issue_id = ?", issueID).Find(&events).Error; err != nil {
		t.Fatalf("list stored timeline events: %v", err)
	}
	result := make(map[string]model.IssueTimelineEvent, len(events))
	for _, e := range events {
		result[e.EventKey] = e
	}
	return result
}

func TestIssueServiceSyncProjectIssues_ImportsIssuesCommentsAndTimeline(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, "user-1", "gh-token")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{
			{
				Issue: gh.Issue{
					ID:                int64Ptr(101),
					NodeID:            stringPtr("I_kw123"),
					Number:            intPtr(42),
					State:             stringPtr("open"),
					Title:             stringPtr("Crash on launch"),
					Body:              stringPtr("App crashes on startup"),
					HTMLURL:           stringPtr("https://github.com/owner/repo/issues/42"),
					User:              &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
					AuthorAssociation: stringPtr("MEMBER"),
					Labels:            []*gh.Label{{Name: stringPtr("bug"), Color: stringPtr("d73a4a")}},
					Comments:          intPtr(1),
					CreatedAt:         &gh.Timestamp{Time: now.Add(-2 * time.Hour)},
					UpdatedAt:         &gh.Timestamp{Time: now.Add(-1 * time.Hour)},
					Reactions:         &gh.Reactions{TotalCount: intPtr(1), PlusOne: intPtr(1)},
				},
				BodyHTML: stringPtr("<p>App crashes on <strong>startup</strong></p>"),
			},
		},
		comments: map[int][]*ghclient.IssueComment{
			42: {
				{
					IssueComment: gh.IssueComment{
						ID:                int64Ptr(501),
						NodeID:            stringPtr("IC_kw123"),
						Body:              stringPtr("I can reproduce this"),
						HTMLURL:           stringPtr("https://github.com/owner/repo/issues/42#issuecomment-501"),
						User:              &gh.User{Login: stringPtr("bob"), AvatarURL: stringPtr("https://example.com/bob.png")},
						AuthorAssociation: stringPtr("CONTRIBUTOR"),
						CreatedAt:         &gh.Timestamp{Time: now.Add(-30 * time.Minute)},
						UpdatedAt:         &gh.Timestamp{Time: now.Add(-30 * time.Minute)},
					},
					BodyHTML: stringPtr("<p>I can reproduce this</p><p><img src=\"https://example.com/repro.png\" alt=\"repro\"></p>"),
				},
			},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {
				{
					Timeline: gh.Timeline{
						ID:        int64Ptr(701),
						Event:     stringPtr("labeled"),
						Actor:     &gh.User{Login: stringPtr("alice"), AvatarURL: stringPtr("https://example.com/alice.png")},
						Label:     &gh.Label{Name: stringPtr("bug")},
						CreatedAt: &gh.Timestamp{Time: now.Add(-20 * time.Minute)},
					},
				},
			},
		},
	}

	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		return fake
	}

	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("sync project issues: %v", err)
	}

	if result.SyncedIssueCount != 1 || result.SyncedCommentCount != 1 || result.SyncedTimelineCount != 1 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	issues, total, err := svc.issueService.List(project.ID, project.UserID, IssueListFilters{}, 1, 20)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if total != 1 || len(issues) != 1 {
		t.Fatalf("expected 1 synced issue, got total=%d len=%d", total, len(issues))
	}
	if issues[0].Title != "Crash on launch" || issues[0].Github == nil || issues[0].Github.Labels[0].Name != "bug" {
		t.Fatalf("unexpected issue payload: %+v", issues[0])
	}
	// 列表响应不再携带正文（INT-106）；正文持久化改由详情接口断言。
	detail, err := svc.issueService.Get(issues[0].Id, project.UserID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if detail.BodyHtml != "<p>App crashes on <strong>startup</strong></p>" {
		t.Fatalf("expected issue html body to be persisted, got %+v", detail)
	}

	comments, total, err := svc.issueService.ListComments(issues[0].Id, project.UserID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if total != 1 || len(comments) != 1 || comments[0].Author.Login != "bob" {
		t.Fatalf("unexpected comments payload: %+v", comments)
	}
	if comments[0].BodyHtml == "" {
		t.Fatalf("expected comment html body to be persisted, got %+v", comments[0])
	}

	events, total, err := svc.issueService.ListTimeline(issues[0].Id, project.UserID, 1, 20)
	if err != nil {
		t.Fatalf("list timeline: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("unexpected events payload: %+v", events)
	}
	if events[0].Summary != "添加了标签 bug" {
		t.Fatalf("unexpected summary: %+v", events[0])
	}
}

func TestIssueServiceSyncProjectIssues_RejectsNotGitHubConfigured(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-nogit")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
	})

	_, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, user.ID)
	if err == nil {
		t.Fatal("expected error for project without GitHub config, got nil")
	}
	appErr, ok := err.(*errs.AppError)
	if !ok {
		t.Fatalf("expected *errs.AppError, got %T", err)
	}
	if appErr.Code != errs.ErrProjectGitHubNotConfigured.Code {
		t.Errorf("expected code %d, got %d", errs.ErrProjectGitHubNotConfigured.Code, appErr.Code)
	}
}

// 评论与时间线各自跨页聚合后单事务提交；再同步一次靠唯一键幂等，不产生重复行。
func TestIssueServiceSyncProjectIssues_PagedCollectionsCommitAndResyncDedupes(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		commentsPages: map[int][][]*ghclient.IssueComment{
			42: {
				{testGitHubSyncComment(501, "first", now), testGitHubSyncComment(502, "second", now)},
				{testGitHubSyncComment(503, "third", now)},
			},
		},
		timelinePages: map[int][][]*ghclient.TimelineEvent{
			42: {
				{testGitHubSyncTimelineEvent(701, "labeled", now), testGitHubSyncTimelineEvent(702, "closed", now)},
				{testGitHubSyncTimelineEvent(703, "reopened", now)},
			},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		if token != "gh-token" {
			t.Fatalf("unexpected token: %q", token)
		}
		return fake
	}

	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("sync project issues: %v", err)
	}
	if result.SyncedIssueCount != 1 || result.SyncedCommentCount != 3 || result.SyncedTimelineCount != 3 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	issueID := syncedGitHubIssueID(t, svc, project.ID)
	if got := storedCommentsByGitHubID(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected 3 stored comments, got %+v", got)
	}
	if got := storedTimelineByKey(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected 3 stored timeline events, got %+v", got)
	}

	// 重复同步：upsert 撞唯一键更新既有行，集合行数不变。
	result, err = svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("resync project issues: %v", err)
	}
	if result.SyncedCommentCount != 3 || result.SyncedTimelineCount != 3 {
		t.Fatalf("unexpected resync result: %+v", result)
	}
	if got := storedCommentsByGitHubID(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected comments to stay deduplicated after resync, got %+v", got)
	}
	if got := storedTimelineByKey(t, svc, issueID); len(got) != 3 {
		t.Fatalf("expected timeline events to stay deduplicated after resync, got %+v", got)
	}
}

// 评论第二页拉取失败：完整镜像全有或全无——第一页已映射的增/改与远端缺失清理都
// 不提交，评论与时间线两个集合都保持同步前数据，水位不推进。
func TestIssueServiceSyncProjectIssues_CommentPageFailureKeepsCollection(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "old body", now), testGitHubSyncComment(599, "stale", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮模拟远端变更：updated_at 必须推进，否则会被变更判据跳过。
	fake.issues = []*ghclient.Issue{testGitHubSyncIssue(101, 42, now.Add(time.Minute))}
	// 第一页改 500 并新增 501、丢弃 599，第二页失败。
	fake.commentsPages = map[int][][]*ghclient.IssueComment{
		42: {
			{testGitHubSyncComment(500, "new body", now), testGitHubSyncComment(501, "new", now)},
			{testGitHubSyncComment(502, "unreached", now)},
		},
	}
	fake.commentErrs = map[int]map[int]error{42: {2: fmt.Errorf("page 2 boom")}}

	_, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err == nil {
		t.Fatal("expected sync error on second comments page, got nil")
	}
	if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("expected github api error, got %v", err)
	}

	stored := storedCommentsByGitHubID(t, svc, issueID)
	if len(stored) != 2 {
		t.Fatalf("expected comment collection untouched, got %+v", stored)
	}
	if stored[500].Body != "old body" {
		t.Fatalf("expected comment 500 update rolled back, got %q", stored[500].Body)
	}
	if _, ok := stored[501]; ok {
		t.Fatalf("expected comment 501 not inserted, got %+v", stored[501])
	}
	if _, ok := stored[599]; !ok {
		t.Fatalf("expected stale comment 599 kept after failed sync")
	}

	// 取数顺序是先评论后时间线：评论分页失败时时间线取数也不该发出，
	// 镜像与水位都停留在首轮种子值。
	storedTimeline := storedTimelineByKey(t, svc, issueID)
	if len(storedTimeline) != 1 || storedTimeline["gh:700"].EventType != "labeled" {
		t.Fatalf("expected timeline collection untouched after comment fetch failure, got %+v", storedTimeline)
	}
	var meta model.IssueGitHubMeta
	if err := svc.db.First(&meta, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("load github meta: %v", err)
	}
	if meta.CollectionsSyncedUpdatedAt == nil || !meta.CollectionsSyncedUpdatedAt.Equal(now) {
		t.Fatalf("expected watermark kept at seed value %v after comment failure, got %v", now, meta.CollectionsSyncedUpdatedAt)
	}
	if meta.CollectionsSyncedComments != 0 {
		t.Fatalf("expected watermark comments kept at 0, got %d", meta.CollectionsSyncedComments)
	}
	if comments, timeline := fake.countCollectionCalls(); timeline != 1 {
		t.Fatalf("expected timeline fetch skipped after comment fetch failure, got comments=%d timeline=%d", comments, timeline)
	}

	state, err := svc.syncStateRepo.Get(project.ID)
	if err != nil {
		t.Fatalf("load sync state: %v", err)
	}
	if state.Status != model.IssueSyncStatusFailed {
		t.Fatalf("expected failed sync state, got %q", state.Status)
	}
}

// 时间线第二页拉取失败：完整镜像全有或全无——已拉到的评论也不再落库，两个集合
// 都保持同步前数据，水位不推进，下一轮同步整体重试。
func TestIssueServiceSyncProjectIssues_TimelinePageFailureKeepsBothCollections(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "old body", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now), testGitHubSyncTimelineEvent(799, "subscribed", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮模拟远端变更：updated_at 必须推进，否则会被变更判据跳过。
	fake.issues = []*ghclient.Issue{testGitHubSyncIssue(101, 42, now.Add(time.Minute))}
	// 评论集合正常返回（尝试把 500 改成新正文）；时间线第一页把 700 改成
	// closed 并新增 701、丢弃 799，第二页失败。
	fake.comments = map[int][]*ghclient.IssueComment{
		42: {testGitHubSyncComment(500, "would-be new body", now)},
	}
	fake.timelinePages = map[int][][]*ghclient.TimelineEvent{
		42: {
			{testGitHubSyncTimelineEvent(700, "closed", now), testGitHubSyncTimelineEvent(701, "reopened", now)},
			{testGitHubSyncTimelineEvent(702, "unreached", now)},
		},
	}
	fake.timelineErrs = map[int]map[int]error{42: {2: fmt.Errorf("page 2 boom")}}

	_, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err == nil {
		t.Fatal("expected sync error on second timeline page, got nil")
	}
	if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("expected github api error, got %v", err)
	}

	// 全有或全无：时间线取数失败时，已拉到的评论更新同样不落库。
	storedComments := storedCommentsByGitHubID(t, svc, issueID)
	if got := storedComments[500]; got.Body != "old body" {
		t.Fatalf("expected comment 500 to keep first-round body after timeline failure, got %+v", storedComments)
	}

	stored := storedTimelineByKey(t, svc, issueID)
	if len(stored) != 2 {
		t.Fatalf("expected timeline collection untouched, got %+v", stored)
	}
	if stored["gh:700"].EventType != "labeled" {
		t.Fatalf("expected event 700 update rolled back, got %q", stored["gh:700"].EventType)
	}
	if _, ok := stored["gh:701"]; ok {
		t.Fatalf("expected event 701 not inserted")
	}
	if _, ok := stored["gh:799"]; !ok {
		t.Fatalf("expected stale event 799 kept after failed sync")
	}

	// 水位随完整镜像同事务推进：时间线失败本轮不得推进，仍停留在首轮种子值，
	// 下一轮同步才会重试拉取。
	var meta model.IssueGitHubMeta
	if err := svc.db.First(&meta, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("load github meta: %v", err)
	}
	if meta.CollectionsSyncedUpdatedAt == nil || !meta.CollectionsSyncedUpdatedAt.Equal(now) {
		t.Fatalf("expected watermark kept at seed value %v after timeline failure, got %v", now, meta.CollectionsSyncedUpdatedAt)
	}
	if meta.CollectionsSyncedComments != 0 {
		t.Fatalf("expected watermark comments kept at 0, got %d", meta.CollectionsSyncedComments)
	}
}

// 远端返回空集合：GitHub 镜像行全部清掉；source='internal' 的本地评论保留。
func TestIssueServiceSyncProjectIssues_EmptyRemoteCleansMirrorKeepsInternal(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "mirror", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 本地来源评论（负数合成 ID）与 GitHub 镜像行共用同一张表。
	internal := &model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		Source:          model.IssueSourceInternal,
		GitHubCommentID: -1,
		Body:            "local note",
		AuthorLogin:     user.Username,
		GitHubCreatedAt: now,
		GitHubUpdatedAt: now,
	}
	if err := svc.db.Create(internal).Error; err != nil {
		t.Fatalf("seed internal comment: %v", err)
	}

	// 第二轮模拟远端变更（清空集合）：updated_at 必须推进，否则会被变更判据跳过。
	fake.issues = []*ghclient.Issue{testGitHubSyncIssue(101, 42, now.Add(time.Minute))}
	fake.comments = nil
	fake.timeline = nil

	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("resync with empty remote: %v", err)
	}
	if result.SyncedCommentCount != 0 || result.SyncedTimelineCount != 0 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	stored := storedCommentsByGitHubID(t, svc, issueID)
	if len(stored) != 1 {
		t.Fatalf("expected only the internal comment to remain, got %+v", stored)
	}
	kept, ok := stored[-1]
	if !ok || kept.ID != internal.ID || kept.Source != model.IssueSourceInternal {
		t.Fatalf("expected internal comment preserved, got %+v", stored)
	}

	if got := storedTimelineByKey(t, svc, issueID); len(got) != 0 {
		t.Fatalf("expected timeline mirror cleaned, got %+v", got)
	}
}

// 并发触发第二次同步：直接被拒（40907），不走到 client 工厂；期间状态保持
// running 且不带错误。首轮放行后成功，随后第三次同步也能跑通，证明锁已释放。
func TestIssueServiceSyncProjectIssues_ConcurrentSyncRejected(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once, releaseOnce sync.Once
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
	}
	fake.onListIssues = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	// 中途断言失败也要放行首个同步 goroutine，避免它挂着阻塞随测试收尾拆除 DB。
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	var factoryCalls atomic.Int32
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		factoryCalls.Add(1)
		return fake
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, user.ID)
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first sync did not reach ListIssues")
	}

	_, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, user.ID)
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrIssueSyncRunning.Code {
		t.Fatalf("expected sync-running error, got %v", err)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("expected rejected sync to not reach client factory, got %d calls", got)
	}

	state, err := svc.syncStateRepo.Get(project.ID)
	if err != nil {
		t.Fatalf("load sync state: %v", err)
	}
	if state.Status != model.IssueSyncStatusRunning || state.LastError != "" {
		t.Fatalf("expected running state without error, got %+v", state)
	}

	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, user.ID); err != nil {
		t.Fatalf("sync after release should succeed: %v", err)
	}
}

// 同步失败后锁必须释放：注入评论分页失败使首轮失败，移除后第二轮成功。
func TestIssueServiceSyncProjectIssues_FailureReleasesLock(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues:      []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		commentErrs: map[int]map[int]error{42: {1: errors.New("comments down")}},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err == nil {
		t.Fatal("expected sync error, got nil")
	}
	state, err := svc.syncStateRepo.Get(project.ID)
	if err != nil {
		t.Fatalf("load sync state: %v", err)
	}
	if state.Status != model.IssueSyncStatusFailed {
		t.Fatalf("expected failed sync state, got %q", state.Status)
	}

	fake.commentErrs = nil
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("sync after failure should succeed: %v", err)
	}
}

// 增量同步：首轮 since 为 nil，次轮带首轮最大 updated_at 减一秒的回退；
// 远端返回空集时不推进也不清空 LastIssueUpdatedAt。
func TestIssueServiceSyncProjectIssues_IncrementalSince(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC().Truncate(time.Second)
	older := now.Add(-time.Hour)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{
			testGitHubSyncIssue(101, 42, older),
			testGitHubSyncIssue(102, 43, now),
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	sinceCalls := fake.recordedSinceCalls()
	if len(sinceCalls) != 1 || sinceCalls[0] != nil {
		t.Fatalf("expected first sync since=nil, got %+v", sinceCalls)
	}

	state, err := svc.syncStateRepo.Get(project.ID)
	if err != nil {
		t.Fatalf("load sync state: %v", err)
	}
	if state.LastIssueUpdatedAt == nil || !state.LastIssueUpdatedAt.Equal(now) {
		t.Fatalf("expected last_issue_updated_at=%v, got %v", now, state.LastIssueUpdatedAt)
	}

	fake.issues = nil
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	sinceCalls = fake.recordedSinceCalls()
	if len(sinceCalls) != 2 {
		t.Fatalf("expected two ListIssues calls, got %d", len(sinceCalls))
	}
	want := now.Add(-time.Second)
	if sinceCalls[1] == nil || !sinceCalls[1].Equal(want) {
		t.Fatalf("expected second sync since=%v, got %v", want, sinceCalls[1])
	}

	state, err = svc.syncStateRepo.Get(project.ID)
	if err != nil {
		t.Fatalf("reload sync state: %v", err)
	}
	if state.LastIssueUpdatedAt == nil || !state.LastIssueUpdatedAt.Equal(now) {
		t.Fatalf("expected last_issue_updated_at kept at %v after empty sync, got %v", now, state.LastIssueUpdatedAt)
	}
}

// 远端无变更的第二次同步：不请求评论/时间线接口，响应计数取现有镜像行数。
func TestIssueServiceSyncProjectIssues_UnchangedRemoteSkipsCollectionFetch(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	item := testGitHubSyncIssue(101, 42, now)
	item.Comments = intPtr(2)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{item},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now), testGitHubSyncComment(501, "b", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if result.SyncedCommentCount != 2 || result.SyncedTimelineCount != 1 {
		t.Fatalf("unexpected first sync result: %+v", result)
	}

	result, err = svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if result.SyncedIssueCount != 1 || result.SyncedCommentCount != 2 || result.SyncedTimelineCount != 1 {
		t.Fatalf("unexpected second sync result: %+v", result)
	}
	if comments, timeline := fake.countCollectionCalls(); comments != 1 || timeline != 1 {
		t.Fatalf("expected collection fetches only in the first sync, got comments=%d timeline=%d", comments, timeline)
	}
}

// 水位为空（改动前留下的旧 meta 行）：同步仍重拉一次评论/时间线做 bootstrap。
func TestIssueServiceSyncProjectIssues_EmptyWatermarkBootstraps(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 模拟无水位列的旧数据：meta 在、水位为空。
	if err := svc.db.Model(&model.IssueGitHubMeta{}).
		Where("issue_id = ?", issueID).
		Updates(map[string]any{"collections_synced_updated_at": nil, "collections_synced_comments": 0}).Error; err != nil {
		t.Fatalf("clear collections watermark: %v", err)
	}

	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}
	if result.SyncedCommentCount != 1 {
		t.Fatalf("unexpected bootstrap sync result: %+v", result)
	}
	if comments, timeline := fake.countCollectionCalls(); comments != 2 || timeline != 2 {
		t.Fatalf("expected bootstrap sync to refetch collections, got comments=%d timeline=%d", comments, timeline)
	}
}

// updated_at 相同但远端评论数变化（同一秒内新增评论）：仍会重拉集合。
func TestIssueServiceSyncProjectIssues_CommentCountChangeRefetches(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC()
	item := testGitHubSyncIssue(101, 42, now)
	item.Comments = intPtr(1)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{item},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// 同一秒新增一条评论：updated_at 不变，远端评论数 1→2。
	updated := testGitHubSyncIssue(101, 42, now)
	updated.Comments = intPtr(2)
	fake.issues = []*ghclient.Issue{updated}
	fake.comments = map[int][]*ghclient.IssueComment{
		42: {testGitHubSyncComment(500, "a", now), testGitHubSyncComment(501, "b", now)},
	}

	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if result.SyncedCommentCount != 2 {
		t.Fatalf("unexpected second sync result: %+v", result)
	}
	if comments, _ := fake.countCollectionCalls(); comments != 2 {
		t.Fatalf("expected comment refetch on comments-count change, got %d calls", comments)
	}
}

// 集合拉取失败不推进水位：错误消失后下一轮同步仍重试拉取。
func TestIssueServiceSyncProjectIssues_FailedCollectionFetchKeepsWatermark(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC().Truncate(time.Second)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮模拟远端变更（updated_at 推进），但评论拉取失败：水位不推进。
	fake.issues = []*ghclient.Issue{testGitHubSyncIssue(101, 42, now.Add(time.Minute))}
	fake.comments = map[int][]*ghclient.IssueComment{
		42: {testGitHubSyncComment(500, "a", now), testGitHubSyncComment(501, "b", now)},
	}
	fake.commentErrs = map[int]map[int]error{42: {1: errors.New("comments down")}}
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err == nil {
		t.Fatal("expected sync error, got nil")
	}

	fake.commentErrs = nil
	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	if result.SyncedCommentCount != 2 {
		t.Fatalf("unexpected retry sync result: %+v", result)
	}
	// 评论 = 首轮 + 失败轮 + 重试轮共 3 次；时间线失败轮未到达，共 2 次。
	if comments, timeline := fake.countCollectionCalls(); comments != 3 || timeline != 2 {
		t.Fatalf("expected collection fetches retried after failure, got comments=%d timeline=%d", comments, timeline)
	}

	var meta model.IssueGitHubMeta
	if err := svc.db.First(&meta, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("load github meta: %v", err)
	}
	if meta.CollectionsSyncedUpdatedAt == nil || !meta.CollectionsSyncedUpdatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("expected watermark advanced to %v, got %v", now.Add(time.Minute), meta.CollectionsSyncedUpdatedAt)
	}
}

// push 路径的时间线刷新只重拉了时间线、没有重拉评论：同一事务里清空集合水位
// 而不是推进——若推进，push 的时间线快照会与「已同步」的水位共存，缺失内容
// 再也无法被判据发现。清空后即使远端再无变化，下一次后台同步也要保守重拉两个
// 集合，这是 push 扰动后的自愈路径。
func TestIssueServiceRefreshTimelineAfterPush_ClearsWatermarkSoNextSyncRefetches(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC().Truncate(time.Second)
	item := testGitHubSyncIssue(101, 42, now)
	item.Comments = intPtr(1)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{item},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)
	stored, err := svc.issueRepo.FindByID(issueID)
	if err != nil {
		t.Fatalf("load stored issue: %v", err)
	}

	var meta model.IssueGitHubMeta
	if err := svc.db.First(&meta, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("load github meta: %v", err)
	}
	if meta.CollectionsSyncedUpdatedAt == nil {
		t.Fatal("expected seed sync to advance the collections watermark")
	}

	if err := svc.issueService.mirror.refreshTimelineAfterPush(context.Background(), fake, stored, item); err != nil {
		t.Fatalf("refresh timeline after push: %v", err)
	}
	// 换新结构体重读：GORM 扫 NULL 到已复用的 *time.Time 字段不会置回 nil。
	var cleared model.IssueGitHubMeta
	if err := svc.db.First(&cleared, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("reload github meta: %v", err)
	}
	meta = cleared
	if meta.CollectionsSyncedUpdatedAt != nil {
		t.Fatalf("expected collections watermark cleared after push, got %v", meta.CollectionsSyncedUpdatedAt)
	}
	if meta.CollectionsSyncedComments != 0 {
		t.Fatalf("expected collections watermark comments cleared after push, got %d", meta.CollectionsSyncedComments)
	}

	// 远端再无变化（updated_at/评论数与首轮一致），下一轮同步因水位为空仍重拉两个集合。
	beforeComments, beforeTimeline := fake.countCollectionCalls()
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("sync after push: %v", err)
	}
	afterComments, afterTimeline := fake.countCollectionCalls()
	if afterComments != beforeComments+1 || afterTimeline != beforeTimeline+1 {
		t.Fatalf("expected next sync to refetch both collections after push cleared the watermark, got comments %d→%d timeline %d→%d",
			beforeComments, afterComments, beforeTimeline, afterTimeline)
	}
	if calls := fake.countGetIssueCalls(); calls != 0 {
		t.Fatalf("expected the list path to do the recovery (no GetIssue calls), got %d", calls)
	}
}

// push 路径的评论写回（recordPushedComment）同样是「只写一部分集合」：三处写入
// 必须与集合水位清空同事务。否则延迟的创建响应覆盖已镜像正文后，远端 (updated_at,
// 评论数) 与水位一致，跳过判据再也发现不了该 issue，旧正文不再自愈；清空后该
// issue 下次被增量列表返回时重拉两个集合，把镜像修回远端值。
func TestIssueServiceRecordPushedComment_ClearsWatermarkAndNextSyncHeals(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC().Truncate(time.Second)
	item := testGitHubSyncIssue(101, 42, now)
	item.Comments = intPtr(1)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{item},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "remote", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)
	stored, err := svc.issueRepo.FindByID(issueID)
	if err != nil {
		t.Fatalf("load stored issue: %v", err)
	}
	var meta model.IssueGitHubMeta
	if err := svc.db.First(&meta, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("load github meta: %v", err)
	}
	if meta.CollectionsSyncedUpdatedAt == nil || meta.CollectionsSyncedComments != 1 {
		t.Fatalf("expected seed sync to advance the collections watermark, got %v / %d", meta.CollectionsSyncedUpdatedAt, meta.CollectionsSyncedComments)
	}

	// 延迟的创建响应：同一个 github_comment_id=500，正文落后于远端已镜像的值。
	pushed := buildGitHubIssueCommentModel(stored.ID, testGitHubSyncComment(500, "stale-push", now))
	if err := svc.issueService.mirror.recordPushedComment(stored, pushed); err != nil {
		t.Fatalf("record pushed comment: %v", err)
	}
	comments := storedCommentsByGitHubID(t, svc, issueID)
	if got := comments[500]; got.Body != "stale-push" {
		t.Fatalf("expected push response to overwrite mirrored body, got %q", got.Body)
	}

	// 换新结构体重读：GORM 扫 NULL 到已复用的 *time.Time 字段不会置回 nil。
	var cleared model.IssueGitHubMeta
	if err := svc.db.First(&cleared, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("reload github meta: %v", err)
	}
	if cleared.CollectionsSyncedUpdatedAt != nil || cleared.CollectionsSyncedComments != 0 {
		t.Fatalf("expected collections watermark cleared after push, got %v / %d", cleared.CollectionsSyncedUpdatedAt, cleared.CollectionsSyncedComments)
	}

	// 远端无变化：水位为空让下一轮同步重拉两个集合，评论正文修回远端值。
	beforeComments, beforeTimeline := fake.countCollectionCalls()
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("sync after push: %v", err)
	}
	afterComments, afterTimeline := fake.countCollectionCalls()
	if afterComments != beforeComments+1 || afterTimeline != beforeTimeline+1 {
		t.Fatalf("expected next sync to refetch both collections after push cleared the watermark, got comments %d→%d timeline %d→%d",
			beforeComments, afterComments, beforeTimeline, afterTimeline)
	}
	if calls := fake.countGetIssueCalls(); calls != 0 {
		t.Fatalf("expected the list path to do the recovery (no GetIssue calls), got %d", calls)
	}
	comments = storedCommentsByGitHubID(t, svc, issueID)
	if got := comments[500]; got.Body != "remote" {
		t.Fatalf("expected comment 500 healed back to remote body, got %q", got.Body)
	}
}

// markCollectionsStaleAfterPush 是「仅状态变更」push（关闭/重开）的水位处理
// （INT-105）：不做任何网络取数，只在同一事务里清空集合水位；下一次同步因水位
// 为空保守重拉两个集合，closed 等时间线事件由此补齐。
func TestIssueServiceMarkCollectionsStaleAfterPush_ClearsWatermarkSoNextSyncRefetches(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	item := testGitHubSyncIssue(101, 42, now)
	item.Comments = intPtr(1)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{item},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	if err := svc.issueService.mirror.markCollectionsStaleAfterPush(issueID); err != nil {
		t.Fatalf("mark collections stale after push: %v", err)
	}
	// 换新结构体重读：GORM 扫 NULL 到已复用的 *time.Time 字段不会置回 nil。
	var cleared model.IssueGitHubMeta
	if err := svc.db.First(&cleared, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("reload github meta: %v", err)
	}
	if cleared.CollectionsSyncedUpdatedAt != nil || cleared.CollectionsSyncedComments != 0 {
		t.Fatalf("expected collections watermark cleared, got %v / %d", cleared.CollectionsSyncedUpdatedAt, cleared.CollectionsSyncedComments)
	}

	// 远端无变化：水位为空让下一轮同步重拉两个集合。
	beforeComments, beforeTimeline := fake.countCollectionCalls()
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("sync after mark stale: %v", err)
	}
	afterComments, afterTimeline := fake.countCollectionCalls()
	if afterComments != beforeComments+1 || afterTimeline != beforeTimeline+1 {
		t.Fatalf("expected next sync to refetch both collections after mark stale, got comments %d→%d timeline %d→%d",
			beforeComments, afterComments, beforeTimeline, afterTimeline)
	}
	if calls := fake.countGetIssueCalls(); calls != 0 {
		t.Fatalf("expected the list path to do the recovery (no GetIssue calls), got %d", calls)
	}
}

// 看板「关闭全部」对 GitHub 来源 issue 走仅状态变更的 push（INT-105）：每条只发
// 一次状态 PATCH、整个批量过程不拉评论/时间线，只清空集合水位；closed 时间线
// 事件由下一次增量同步补齐。
func TestIssueServiceBatchCloseDoneIssues_GitHubIssuesCloseWithOnePatchEachAndNoTimelineFetch(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{
			testGitHubSyncIssue(101, 42, now),
			testGitHubSyncIssue(102, 43, now),
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
			43: {testGitHubSyncTimelineEvent(710, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	var metas []model.IssueGitHubMeta
	if err := svc.db.Where("project_id = ?", project.ID).Order("number ASC").Find(&metas).Error; err != nil {
		t.Fatalf("load synced metas: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 synced github issues, got %d", len(metas))
	}
	// 批量关闭按「open + workflow done」过滤，GitHub issue 也读内部 meta 的
	// workflow_status：直接写库把它标成 done。
	for _, m := range metas {
		meta := &model.IssueInternalMeta{
			IssueID:         m.IssueID,
			WorkflowStatus:  model.IssueWorkflowStatusDone,
			UpdatedByUserID: user.ID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := svc.db.Create(meta).Error; err != nil {
			t.Fatalf("create internal meta: %v", err)
		}
	}

	// 每条 issue 各自的关闭响应：写回路径按 GitHub 响应里的 issue ID 定位本地行。
	closedAt := now.Add(time.Minute)
	issueIDByNumber := make(map[int]string, len(metas))
	for _, m := range metas {
		item := testGitHubSyncIssue(m.GitHubIssueID, m.Number, closedAt)
		item.State = stringPtr("closed")
		item.StateReason = stringPtr("completed")
		item.ClosedAt = &gh.Timestamp{Time: closedAt}
		if fake.updatedIssuesByNumber == nil {
			fake.updatedIssuesByNumber = map[int]*ghclient.Issue{}
		}
		fake.updatedIssuesByNumber[m.Number] = item
		issueIDByNumber[m.Number] = m.IssueID
	}

	beforeComments, beforeTimeline := fake.countCollectionCalls()
	resp, err := svc.issueService.BatchCloseDoneIssues(project.ID, user.ID, string(model.IssueSourceGitHub))
	if err != nil {
		t.Fatalf("batch close done issues: %v", err)
	}
	if resp.Total != 2 || resp.Succeeded != 2 || resp.Failed != 0 || len(resp.Failures) != 0 {
		t.Fatalf("unexpected batch close result: %+v", resp)
	}
	if len(fake.updateIssueCalls) != 2 {
		t.Fatalf("expected one github update call per issue, got %d", len(fake.updateIssueCalls))
	}
	calledNumbers := map[int]bool{}
	for _, call := range fake.updateIssueCalls {
		calledNumbers[call.IssueNumber] = true
		if call.State != "closed" || call.StateReason != "completed" {
			t.Fatalf("unexpected update call: %+v", call)
		}
	}
	if !calledNumbers[42] || !calledNumbers[43] || len(calledNumbers) != 2 {
		t.Fatalf("expected update calls for issues 42 and 43, got %v", calledNumbers)
	}
	// 批量过程一个集合拉取都不发生（种子同步留下的计数不增长）。
	if comments, timeline := fake.countCollectionCalls(); comments != beforeComments || timeline != beforeTimeline {
		t.Fatalf("expected batch close to fetch no collections, got comments %d→%d timeline %d→%d",
			beforeComments, comments, beforeTimeline, timeline)
	}
	for _, m := range metas {
		// 换新结构体重读：GORM 扫 NULL 到已复用的 *time.Time 字段不会置回 nil。
		var cleared model.IssueGitHubMeta
		if err := svc.db.First(&cleared, "issue_id = ?", m.IssueID).Error; err != nil {
			t.Fatalf("reload github meta: %v", err)
		}
		if cleared.CollectionsSyncedUpdatedAt != nil || cleared.CollectionsSyncedComments != 0 {
			t.Fatalf("expected collections watermark cleared for issue %d, got %v / %d", m.Number, cleared.CollectionsSyncedUpdatedAt, cleared.CollectionsSyncedComments)
		}
	}

	// 远端补上 closed 时间线事件后，下一次增量同步重拉两个集合并推进水位。
	fake.issues = []*ghclient.Issue{
		fake.updatedIssuesByNumber[42],
		fake.updatedIssuesByNumber[43],
	}
	fake.timeline = map[int][]*ghclient.TimelineEvent{
		42: {
			testGitHubSyncTimelineEvent(700, "labeled", now),
			testGitHubSyncTimelineEvent(701, "closed", closedAt),
		},
		43: {
			testGitHubSyncTimelineEvent(710, "labeled", now),
			testGitHubSyncTimelineEvent(711, "closed", closedAt),
		},
	}
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("sync after batch close: %v", err)
	}
	if comments, timeline := fake.countCollectionCalls(); comments != beforeComments+2 || timeline != beforeTimeline+2 {
		t.Fatalf("expected next sync to refetch both collections for both issues, got comments %d→%d timeline %d→%d",
			beforeComments, comments, beforeTimeline, timeline)
	}
	closedEventKeyByNumber := map[int]string{42: "gh:701", 43: "gh:711"}
	for number, issueID := range issueIDByNumber {
		var refreshed model.IssueGitHubMeta
		if err := svc.db.First(&refreshed, "issue_id = ?", issueID).Error; err != nil {
			t.Fatalf("reload github meta after sync: %v", err)
		}
		if refreshed.CollectionsSyncedUpdatedAt == nil {
			t.Fatalf("expected sync to re-advance the collections watermark for issue %d", number)
		}
		events := storedTimelineByKey(t, svc, issueID)
		wantKey, ok := closedEventKeyByNumber[number]
		if !ok {
			t.Fatalf("unexpected synced issue number %d", number)
		}
		if _, ok := events[wantKey]; !ok {
			t.Fatalf("expected closed timeline event %s for issue %d, got %v", wantKey, number, keysOf(events))
		}
	}
}

func keysOf(events map[string]model.IssueTimelineEvent) []string {
	keys := make([]string, 0, len(events))
	for key := range events {
		keys = append(keys, key)
	}
	return keys
}

// 逃逸窗口回归（INT-105）：水位被 state-only push 清空的 issue 若落到增量窗口
// 之外（该轮增量列表不再返回它），恢复不能只等增量列表——healStaleCollections
// 兜底重拉它的两个集合、推进水位、补上 closed 时间线事件。
func TestIssueServiceSync_HealsStaleCollectionsEvenWhenIssueNotListed(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	item := testGitHubSyncIssue(101, 42, now)
	item.Comments = intPtr(1)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{item},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// state-only push（等价于看板关闭全部/发货钩子关单的写回）清空集合水位。
	if err := svc.issueService.mirror.markCollectionsStaleAfterPush(issueID); err != nil {
		t.Fatalf("mark collections stale after push: %v", err)
	}

	// 远端补上 closed 事件，但该 issue 落到增量窗口之外：列表不再返回它
	// （并发 Sync 已用更大的 maxIssueUpdatedAt 推进游标的场景）。兜底经 GetIssue
	// 取真实远端快照做水位。
	closedAt := now.Add(time.Minute)
	closedItem := testGitHubSyncIssue(101, 42, closedAt)
	closedItem.Comments = intPtr(1)
	fake.issues = nil
	fake.issuesByNumber = map[int]*ghclient.Issue{42: closedItem}
	fake.timeline = map[int][]*ghclient.TimelineEvent{
		42: {
			testGitHubSyncTimelineEvent(700, "labeled", now),
			testGitHubSyncTimelineEvent(701, "closed", closedAt),
		},
	}

	beforeComments, beforeTimeline := fake.countCollectionCalls()
	result, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID)
	if err != nil {
		t.Fatalf("healing sync: %v", err)
	}
	afterComments, afterTimeline := fake.countCollectionCalls()
	if afterComments != beforeComments+1 || afterTimeline != beforeTimeline+1 {
		t.Fatalf("expected heal to refetch both collections for the unlisted issue, got comments %d→%d timeline %d→%d",
			beforeComments, afterComments, beforeTimeline, afterTimeline)
	}

	if calls := fake.countGetIssueCalls(); calls != 1 {
		t.Fatalf("expected heal to fetch the remote issue snapshot exactly once, got %d", calls)
	}

	// 水位被兜底推进，且取的是 GetIssue 的远端 updated_at（不是被污染的本地值）。
	var healed model.IssueGitHubMeta
	if err := svc.db.First(&healed, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("reload github meta: %v", err)
	}
	if healed.CollectionsSyncedUpdatedAt == nil || !healed.CollectionsSyncedUpdatedAt.Equal(closedAt) {
		t.Fatalf("expected heal to advance watermark to remote %v, got %v", closedAt, healed.CollectionsSyncedUpdatedAt)
	}

	// closed 时间线事件被补齐。
	events := storedTimelineByKey(t, svc, issueID)
	if _, ok := events["gh:701"]; !ok {
		t.Fatalf("expected closed timeline event gh:701 healed, got %v", keysOf(events))
	}

	// 兜底计入本轮响应计数（与列表路径同口径：本轮镜像的集合大小）。
	if result.SyncedCommentCount != 1 || result.SyncedTimelineCount != 2 {
		t.Fatalf("expected heal counts merged into sync response, got %+v", result)
	}
}

// 兜底有界：单轮 Sync 对空水位 meta 的兜底重拉不超过 staleCollectionsHealLimit 条，
// 其余留到下一轮逐步排空（有界防 GitHub 限流，不是收敛手段）。
func TestIssueServiceSync_HealsAtMostStaleCollectionsHealLimitPerRound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	total := staleCollectionsHealLimit + 2
	issues := make([]*ghclient.Issue, 0, total)
	issuesByNumber := make(map[int]*ghclient.Issue, total)
	comments := make(map[int][]*ghclient.IssueComment, total)
	timeline := make(map[int][]*ghclient.TimelineEvent, total)
	for i := 0; i < total; i++ {
		number := 100 + i
		item := testGitHubSyncIssue(int64(1000+i), number, now)
		issues = append(issues, item)
		issuesByNumber[number] = item
		comments[number] = []*ghclient.IssueComment{testGitHubSyncComment(int64(5000+i), "a", now)}
		timeline[number] = []*ghclient.TimelineEvent{testGitHubSyncTimelineEvent(int64(7000+i), "labeled", now)}
	}
	fake := &fakeIssueGitHubClient{issues: issues, issuesByNumber: issuesByNumber, comments: comments, timeline: timeline}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	var metas []model.IssueGitHubMeta
	if err := svc.db.Where("project_id = ?", project.ID).Order("number ASC").Find(&metas).Error; err != nil {
		t.Fatalf("load synced metas: %v", err)
	}
	if len(metas) != total {
		t.Fatalf("expected %d synced github issues, got %d", total, len(metas))
	}

	// 全部清空水位（模拟批量关闭全部），且增量列表不再返回任何 issue：
	// 整轮恢复只能靠兜底路径。
	for _, m := range metas {
		if err := svc.issueService.mirror.markCollectionsStaleAfterPush(m.IssueID); err != nil {
			t.Fatalf("mark collections stale for issue %d: %v", m.Number, err)
		}
	}
	fake.issues = nil

	beforeComments, beforeTimeline := fake.countCollectionCalls()
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("first healing sync: %v", err)
	}
	afterComments, afterTimeline := fake.countCollectionCalls()
	if afterComments != beforeComments+staleCollectionsHealLimit || afterTimeline != beforeTimeline+staleCollectionsHealLimit {
		t.Fatalf("expected heal bounded to %d issues, got comments %d→%d timeline %d→%d",
			staleCollectionsHealLimit, beforeComments, afterComments, beforeTimeline, afterTimeline)
	}
	var remaining int64
	if err := svc.db.Model(&model.IssueGitHubMeta{}).
		Where("project_id = ? AND collections_synced_updated_at IS NULL", project.ID).
		Count(&remaining).Error; err != nil {
		t.Fatalf("count stale metas: %v", err)
	}
	if remaining != 2 {
		t.Fatalf("expected 2 stale metas left for the next round, got %d", remaining)
	}

	// 下一轮把剩余的排空。
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("second healing sync: %v", err)
	}
	if err := svc.db.Model(&model.IssueGitHubMeta{}).
		Where("project_id = ? AND collections_synced_updated_at IS NULL", project.ID).
		Count(&remaining).Error; err != nil {
		t.Fatalf("count stale metas after second sync: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected all stale metas drained across rounds, got %d", remaining)
	}
}

// 兜底单条失败不拖死整轮：某条空水位对应的远端 issue 取数持久失败（例如已被删除、
// 404）时，只跳过该条并保留空水位待下轮重试，其他空水位条目照常补齐，Sync 仍返回
// 成功——兜底是 backstop，不能让一条持久错误把整个项目同步永久记败、游标停摆。
func TestIssueServiceSync_HealFailureDoesNotFailWholeSync(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	item42 := testGitHubSyncIssue(101, 42, now)
	item43 := testGitHubSyncIssue(102, 43, now)
	fake := &fakeIssueGitHubClient{
		issues:         []*ghclient.Issue{item42, item43},
		issuesByNumber: map[int]*ghclient.Issue{42: item42, 43: item43},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "a", now)},
			43: {testGitHubSyncComment(510, "b", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
			43: {testGitHubSyncTimelineEvent(710, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	var metas []model.IssueGitHubMeta
	if err := svc.db.Where("project_id = ?", project.ID).Order("number ASC").Find(&metas).Error; err != nil {
		t.Fatalf("load synced metas: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 synced github issues, got %d", len(metas))
	}
	issueIDByNumber := make(map[int]string, len(metas))
	for _, m := range metas {
		if err := svc.issueService.mirror.markCollectionsStaleAfterPush(m.IssueID); err != nil {
			t.Fatalf("mark collections stale for issue %d: %v", m.Number, err)
		}
		issueIDByNumber[m.Number] = m.IssueID
	}

	// 列表不再返回任何 issue；42 的评论取数持久失败（如远端已删除），43 正常。
	fake.issues = nil
	fake.commentErrs = map[int]map[int]error{
		42: {1: errs.New(errs.ErrGitHubAPI.Code, "404 Not Found")},
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("sync should not fail because one stale issue cannot be fetched: %v", err)
	}

	assertWatermark := func(number int, wantAdvanced bool) {
		t.Helper()
		var meta model.IssueGitHubMeta
		if err := svc.db.First(&meta, "issue_id = ?", issueIDByNumber[number]).Error; err != nil {
			t.Fatalf("reload meta for issue %d: %v", number, err)
		}
		if wantAdvanced && meta.CollectionsSyncedUpdatedAt == nil {
			t.Fatalf("expected issue %d healed and watermark advanced", number)
		}
		if !wantAdvanced && meta.CollectionsSyncedUpdatedAt != nil {
			t.Fatalf("expected issue %d kept stale for the next round", number)
		}
	}
	assertWatermark(43, true)
	assertWatermark(42, false)

	// 持久错误修好后（远端恢复/issue 不再 404），下一轮把保留的空水位补上。
	delete(fake.commentErrs, 42)
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("retry sync after transient failure: %v", err)
	}
	assertWatermark(42, true)
}

// 完整镜像的失效版本守卫（INT-105）：取数期间发生并发 push（失效版本递增）时，
// 旧的取数快照必须被放弃——不替换集合、也不推进水位；否则会把 push 清空的水位
// 写回非空，抹掉失效信号，之后游标越过该 issue 就再也没人补时间线。
func TestIssueServiceWriteFullCollections_SkipsStaleSnapshotWhenInvalidatedDuringFetch(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	fake := &fakeIssueGitHubClient{
		issues:   []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{42: {testGitHubSyncComment(500, "a", now)}},
		timeline: map[int][]*ghclient.TimelineEvent{42: {testGitHubSyncTimelineEvent(700, "labeled", now)}},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient { return fake }

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	var seeded model.IssueGitHubMeta
	if err := svc.db.First(&seeded, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("load seeded meta: %v", err)
	}
	issue, err := svc.issueService.issueRepo.FindByID(issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	// 模拟取数期间被 push 失效：版本递增、水位清空。
	if err := svc.issueService.mirror.markCollectionsStaleAfterPush(issueID); err != nil {
		t.Fatalf("invalidate collections: %v", err)
	}

	// 用失效前的版本提交旧快照：必须被放弃。
	advanced, err := svc.issueService.mirror.writeFullCollections(
		issue, seeded.CollectionsInvalidatedVersion, now.Add(time.Minute), 0,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("write full collections (stale version): %v", err)
	}
	if advanced {
		t.Fatal("expected stale snapshot to be skipped when invalidated during fetch")
	}
	var afterStale model.IssueGitHubMeta
	if err := svc.db.First(&afterStale, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("reload meta: %v", err)
	}
	if afterStale.CollectionsSyncedUpdatedAt != nil {
		t.Fatalf("expected watermark kept empty after skipped stale snapshot, got %v", afterStale.CollectionsSyncedUpdatedAt)
	}
	// 旧快照必须什么都没写：种子评论与时间线保持原样（只断言空水位的话，错实现
	// 即使替换了集合也能通过）。
	if got := storedCommentsByGitHubID(t, svc, issueID); len(got) != 1 || got[500].Body != "a" {
		t.Fatalf("expected seed comments untouched when stale snapshot is skipped, got %+v", got)
	}
	if events := storedTimelineByKey(t, svc, issueID); len(events) != 1 {
		t.Fatalf("expected seed timeline untouched when stale snapshot is skipped, got %v", keysOf(events))
	}
	// 列表路径与直接完整镜像路径不调 GetIssue；只有兜底会调。
	if calls := fake.countGetIssueCalls(); calls != 0 {
		t.Fatalf("expected no GetIssue calls outside the heal path, got %d", calls)
	}

	// 版本未变时正常推进。
	advanced, err = svc.issueService.mirror.writeFullCollections(
		issue, afterStale.CollectionsInvalidatedVersion, now.Add(time.Minute), 0,
		nil, nil,
	)
	if err != nil {
		t.Fatalf("write full collections (current version): %v", err)
	}
	if !advanced {
		t.Fatal("expected full mirror to advance when the invalidation version is unchanged")
	}
}

// 兜底公平轮转（INT-105）：每轮固定取 staleCollectionsHealLimit 条，候选按上次
// 队列位置升序（NULL 最前），每次实际尝试后把该条目排到队尾；即使最早排队的一整批
// 持续失败，较旧、更久未尝试的可恢复条目也能在后续轮次拿到配额，不会被饿死。
func TestIssueServiceSync_HealsStaleCollectionsFairlyDespitePersistentFailures(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	total := staleCollectionsHealLimit + 1
	issues := make([]*ghclient.Issue, 0, total)
	issuesByNumber := make(map[int]*ghclient.Issue, total)
	comments := make(map[int][]*ghclient.IssueComment, total)
	timeline := make(map[int][]*ghclient.TimelineEvent, total)
	for i := 0; i < total; i++ {
		number := 200 + i
		item := testGitHubSyncIssue(int64(2000+i), number, now)
		issues = append(issues, item)
		issuesByNumber[number] = item
		comments[number] = []*ghclient.IssueComment{testGitHubSyncComment(int64(6000+i), "a", now)}
		timeline[number] = []*ghclient.TimelineEvent{testGitHubSyncTimelineEvent(int64(8000+i), "labeled", now)}
	}
	fake := &fakeIssueGitHubClient{issues: issues, issuesByNumber: issuesByNumber, comments: comments, timeline: timeline}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient { return fake }

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	var metas []model.IssueGitHubMeta
	if err := svc.db.Where("project_id = ?", project.ID).Order("number ASC").Find(&metas).Error; err != nil {
		t.Fatalf("load metas: %v", err)
	}
	if len(metas) != total {
		t.Fatalf("expected %d metas, got %d", total, len(metas))
	}
	// 全部失效；排队位置人工安排：编号 200..200+limit-1 更早排队（now-1h），
	// 最后一个 200+limit 排在它们之后（now）。
	survivor := 200 + staleCollectionsHealLimit
	for _, m := range metas {
		if err := svc.issueService.mirror.markCollectionsStaleAfterPush(m.IssueID); err != nil {
			t.Fatalf("invalidate %d: %v", m.Number, err)
		}
		queuedAt := now
		if m.Number != survivor {
			queuedAt = now.Add(-time.Hour)
		}
		if err := svc.db.Model(&model.IssueGitHubMeta{}).
			Where("issue_id = ?", m.IssueID).
			Update("collections_heal_queued_at", queuedAt).Error; err != nil {
			t.Fatalf("set queued_at for %d: %v", m.Number, err)
		}
	}
	fake.issues = nil
	// 排队最久的一整批（编号 200..200+limit-1）持续失败；最后一个 200+limit 尚可恢复。
	commentErrs := make(map[int]map[int]error, staleCollectionsHealLimit)
	for i := 0; i < staleCollectionsHealLimit; i++ {
		commentErrs[200+i] = map[int]error{1: errs.New(errs.ErrGitHubAPI.Code, "404 Not Found")}
	}
	fake.commentErrs = commentErrs

	// 第一轮：排队最久的一批占满配额并全部失败，200+limit 未进候选。
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	var survivorMeta model.IssueGitHubMeta
	if err := svc.db.First(&survivorMeta, "project_id = ? AND number = ?", project.ID, survivor).Error; err != nil {
		t.Fatalf("load survivor meta: %v", err)
	}
	if survivorMeta.CollectionsSyncedUpdatedAt != nil {
		t.Fatal("expected survivor meta to be skipped in the first round while the longest-waiting ones fill the quota")
	}

	// 第二轮：失败过的条目 attempts_at 已更新、排到后面，从未尝试过的 survivor 先被补齐。
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if err := svc.db.First(&survivorMeta, "project_id = ? AND number = ?", project.ID, survivor).Error; err != nil {
		t.Fatalf("reload survivor meta: %v", err)
	}
	if survivorMeta.CollectionsSyncedUpdatedAt == nil {
		t.Fatal("expected survivor meta to be healed in the second round despite persistent failures of the longest-waiting ones")
	}
}

// 从未尝试过的空水位条目按排队时间先来先服务（INT-105 R4）：即使每轮都有更晚进入
// 的、同样没尝试过的条目，排队更久的旧条目也会先被选中；否则新条目会永久挤出旧条目。
func TestIssueServiceSync_HealQueueServesOldestUntriedFirst(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	total := staleCollectionsHealLimit + 1
	issues := make([]*ghclient.Issue, 0, total)
	issuesByNumber := make(map[int]*ghclient.Issue, total)
	comments := make(map[int][]*ghclient.IssueComment, total)
	timeline := make(map[int][]*ghclient.TimelineEvent, total)
	for i := 0; i < total; i++ {
		number := 400 + i
		item := testGitHubSyncIssue(int64(4000+i), number, now)
		issues = append(issues, item)
		issuesByNumber[number] = item
		comments[number] = []*ghclient.IssueComment{testGitHubSyncComment(int64(8000+i), "a", now)}
		timeline[number] = []*ghclient.TimelineEvent{testGitHubSyncTimelineEvent(int64(9000+i), "labeled", now)}
	}
	fake := &fakeIssueGitHubClient{issues: issues, issuesByNumber: issuesByNumber, comments: comments, timeline: timeline}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient { return fake }

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	var metas []model.IssueGitHubMeta
	if err := svc.db.Where("project_id = ?", project.ID).Order("number ASC").Find(&metas).Error; err != nil {
		t.Fatalf("load metas: %v", err)
	}
	// 全部失效；排队位置随编号递增（400 排队最久），且都从未被兜底尝试。
	for i, m := range metas {
		if err := svc.issueService.mirror.markCollectionsStaleAfterPush(m.IssueID); err != nil {
			t.Fatalf("invalidate %d: %v", m.Number, err)
		}
		if err := svc.db.Model(&model.IssueGitHubMeta{}).
			Where("issue_id = ?", m.IssueID).
			Update("collections_heal_queued_at", now.Add(time.Duration(i)*time.Second)).Error; err != nil {
			t.Fatalf("set queued_at for %d: %v", m.Number, err)
		}
	}
	fake.issues = nil

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("healing sync: %v", err)
	}
	var oldest model.IssueGitHubMeta
	if err := svc.db.First(&oldest, "project_id = ? AND number = ?", project.ID, 400).Error; err != nil {
		t.Fatalf("load oldest meta: %v", err)
	}
	if oldest.CollectionsSyncedUpdatedAt == nil {
		t.Fatal("expected the longest-waiting never-attempted item to be healed first")
	}
}

// 重新失效不重置兜底轮转位置（INT-105）：若实现按「失败计数」排且重新失效时清零，
// 每轮把最新的一批重新失效就会把更久未尝试的旧条目永久挤出候选；按「上次尝试
// 时间」排序则不会——先写尝试时间、再重新失效，最久未尝试的条目仍会被优先选中。
func TestIssueServiceSync_HealRotationIsNotResetByReinvalidation(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	now := time.Now().UTC().Truncate(time.Second)
	total := staleCollectionsHealLimit + 1
	issues := make([]*ghclient.Issue, 0, total)
	issuesByNumber := make(map[int]*ghclient.Issue, total)
	comments := make(map[int][]*ghclient.IssueComment, total)
	timeline := make(map[int][]*ghclient.TimelineEvent, total)
	for i := 0; i < total; i++ {
		number := 300 + i
		item := testGitHubSyncIssue(int64(3000+i), number, now)
		issues = append(issues, item)
		issuesByNumber[number] = item
		comments[number] = []*ghclient.IssueComment{testGitHubSyncComment(int64(7000+i), "a", now)}
		timeline[number] = []*ghclient.TimelineEvent{testGitHubSyncTimelineEvent(int64(9000+i), "labeled", now)}
	}
	fake := &fakeIssueGitHubClient{issues: issues, issuesByNumber: issuesByNumber, comments: comments, timeline: timeline}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient { return fake }

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	var metas []model.IssueGitHubMeta
	if err := svc.db.Where("project_id = ?", project.ID).Order("number ASC").Find(&metas).Error; err != nil {
		t.Fatalf("load metas: %v", err)
	}
	if len(metas) != total {
		t.Fatalf("expected %d metas, got %d", total, len(metas))
	}
	// 先全部失效；再把 300 的排队位置设为最早（now-2h）、其余为 now-1h。随后只对
	// 300 再来一次「等待中的 push」：正确的实现不改变它的队列位置，错误的实现
	// （每次失效都重置当前时间）会把它送到队尾、被挤出候选。
	for _, m := range metas {
		if err := svc.issueService.mirror.markCollectionsStaleAfterPush(m.IssueID); err != nil {
			t.Fatalf("invalidate %d: %v", m.Number, err)
		}
		queuedAt := now.Add(-time.Hour)
		if m.Number == 300 {
			queuedAt = now.Add(-2 * time.Hour)
		}
		if err := svc.db.Model(&model.IssueGitHubMeta{}).
			Where("issue_id = ?", m.IssueID).
			Update("collections_heal_queued_at", queuedAt).Error; err != nil {
			t.Fatalf("set queued_at for %d: %v", m.Number, err)
		}
	}
	// number ASC 下 metas[0] 就是 300。
	if err := svc.issueService.mirror.markCollectionsStaleAfterPush(metas[0].IssueID); err != nil {
		t.Fatalf("re-invalidate oldest: %v", err)
	}
	fake.issues = nil

	// 一轮兜底：等待中被重新失效的 300 应保持最早排队位置、优先被补齐。
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("healing sync: %v", err)
	}
	var oldest model.IssueGitHubMeta
	if err := svc.db.First(&oldest, "project_id = ? AND number = ?", project.ID, 300).Error; err != nil {
		t.Fatalf("load oldest meta: %v", err)
	}
	if oldest.CollectionsSyncedUpdatedAt == nil {
		t.Fatal("expected the longest-waiting item to keep its queue position and be healed first despite a waiting push")
	}
}

// 确定性回归（INT-104 第三轮 C<P<T 反例，INT-105 后机制更新）：push 评论写回
// 落在评论取数之后、完整镜像写库事务之前。`recordPushedComment` 会让失效版本
// 自增，因此该轮完整镜像事务因版本不符整体回滚（旧快照一个字也不写），同轮
// `healStaleCollections` 再取数并写入——终态正文 "remote" 与推进的水位由兜底
// 写入，而不是完整镜像事务。不论哪条路径，都不存在「旧镜像 + 已确认水位」。
func TestIssueServiceSyncProjectIssues_PushWriteBetweenFetchesIsOverwrittenByFullMirror(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	token := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "gh-token")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = token.ID
		p.CodeGitHubTokenID = token.ID
	})

	now := time.Now().UTC().Truncate(time.Second)
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "remote", now)},
		},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮模拟远端变更（updated_at 推进、评论数 1），并在时间线取数开头注入
	// push 写回：此时评论已取完（远端正文），写库事务尚未开始——push 的延迟
	// 响应（同 github_comment_id=500、旧正文）先落库并清空水位。
	updated := testGitHubSyncIssue(101, 42, now.Add(time.Minute))
	updated.Comments = intPtr(1)
	fake.issues = []*ghclient.Issue{updated}
	var once sync.Once
	fake.onListTimeline = func() {
		once.Do(func() {
			var issue model.Issue
			if err := svc.db.First(&issue, "project_id = ?", project.ID).Error; err != nil {
				t.Fatalf("load issue in callback: %v", err)
			}
			stored, err := svc.issueRepo.FindByID(issue.ID)
			if err != nil {
				t.Fatalf("load issue with meta in callback: %v", err)
			}
			pushed := buildGitHubIssueCommentModel(stored.ID, testGitHubSyncComment(500, "stale-push", now))
			if err := svc.issueService.mirror.recordPushedComment(stored, pushed); err != nil {
				t.Fatalf("record pushed comment in callback: %v", err)
			}
		})
	}

	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("sync with injected push write: %v", err)
	}

	// 完整镜像事务覆盖 push 写回：终态正文 = 远端值，不再是 stale-push。
	stored := storedCommentsByGitHubID(t, svc, issueID)
	if got := stored[500]; got.Body != "remote" {
		t.Fatalf("expected full mirror to overwrite stale push body, got %q", got.Body)
	}

	// 水位已推进到本轮列表快照：非 nil，评论数等于 item.GetComments()。
	var meta model.IssueGitHubMeta
	if err := svc.db.First(&meta, "issue_id = ?", issueID).Error; err != nil {
		t.Fatalf("load github meta: %v", err)
	}
	if meta.CollectionsSyncedUpdatedAt == nil || !meta.CollectionsSyncedUpdatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("expected watermark advanced to list snapshot %v, got %v", now.Add(time.Minute), meta.CollectionsSyncedUpdatedAt)
	}
	if meta.CollectionsSyncedComments != updated.GetComments() {
		t.Fatalf("expected watermark comments=%d, got %d", updated.GetComments(), meta.CollectionsSyncedComments)
	}

	// 远端不再变化：判据认为该 issue 已完整镜像，下一轮不请求评论/时间线接口。
	beforeComments, beforeTimeline := fake.countCollectionCalls()
	if _, err := svc.issueService.SyncProjectIssues(context.Background(), project.ID, project.UserID); err != nil {
		t.Fatalf("second sync after full mirror: %v", err)
	}
	afterComments, afterTimeline := fake.countCollectionCalls()
	if afterComments != beforeComments || afterTimeline != beforeTimeline {
		t.Fatalf("expected no collection refetch once fully mirrored, got comments %d→%d timeline %d→%d",
			beforeComments, afterComments, beforeTimeline, afterTimeline)
	}
}

// SyncAll 跳过未配置 GitHub 的项目，单项目失败不中断后续项目。
func TestIssueMirrorSyncAll_SkipsUnconfiguredAndContinuesAfterFailure(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")

	now := time.Now().UTC()
	unconfigured := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
	})
	healthyToken := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "tok-ok")
	healthy := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = healthyToken.ID
		p.CodeGitHubTokenID = healthyToken.ID
		p.CreatedAt = now.Add(-time.Minute)
	})
	// ListAll 按 created_at DESC，失败项目排最前先同步。
	failingToken := createTestToken(t, svc.db, svc.cfg.Encryption.Key, user.ID, "tok-fail")
	failing := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.PublicGitHubTokenID = failingToken.ID
		p.CodeGitHubTokenID = failingToken.ID
		p.CreatedAt = now
	})

	healthyFake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(201, 7, now)},
	}
	// 记录工厂调用顺序：只有「失败项目先于健康项目同步」成立，「单项目失败
	// 不中断后续」才算被真正测到。
	var tokensMu sync.Mutex
	var factoryTokens []string
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		tokensMu.Lock()
		factoryTokens = append(factoryTokens, token)
		tokensMu.Unlock()
		switch token {
		case "tok-ok":
			return healthyFake
		case "tok-fail":
			return &fakeIssueGitHubClient{validateErr: errors.New("repo gone")}
		default:
			t.Fatalf("unexpected token: %q", token)
			return nil
		}
	}

	svc.issueService.SyncAllProjectsIncremental(context.Background())

	tokensMu.Lock()
	gotTokens := append([]string(nil), factoryTokens...)
	tokensMu.Unlock()
	if len(gotTokens) != 2 || gotTokens[0] != "tok-fail" || gotTokens[1] != "tok-ok" {
		t.Fatalf("expected sync order [tok-fail tok-ok] (failure does not stop later project), got %v", gotTokens)
	}
	if id := syncedGitHubIssueID(t, svc, healthy.ID); id == "" {
		t.Fatalf("expected issue synced for healthy project")
	}
	var count int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ?", unconfigured.ID).Count(&count).Error; err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no issues for unconfigured project, got %d", count)
	}
	// 未配置项目必须在 Sync 外被跳过：进入 Sync 会先建/写 issue_sync_states，
	// 断言无该行才能证明 IsGitHubConfigured 短路生效。
	if _, err := svc.syncStateRepo.Get(unconfigured.ID); err == nil {
		t.Fatal("expected no sync state row for unconfigured project")
	}
	state, err := svc.syncStateRepo.Get(failing.ID)
	if err != nil {
		t.Fatalf("load failing sync state: %v", err)
	}
	if state.Status != model.IssueSyncStatusFailed {
		t.Fatalf("expected failed state for failing project, got %q", state.Status)
	}
}

func TestSyncRepositoryLabels(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	projectID := createTestProject(t, svc.db, user.ID).ID

	fake := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("bug"), Color: stringPtr("d73a4a"), Description: stringPtr("Bug desc")},
			{Name: stringPtr("feature"), Color: stringPtr("a2eeef"), Description: stringPtr("Feature desc")},
		},
	}

	err := svc.issueService.mirror.syncRepositoryLabels(context.Background(), fake, projectID)
	if err != nil {
		t.Fatalf("sync repository labels: %v", err)
	}

	cached, err := svc.issueService.githubRepoLabelRepo.ListByProject(projectID)
	if err != nil {
		t.Fatalf("list cached labels: %v", err)
	}
	if len(cached) != 2 {
		t.Fatalf("expected 2 cached labels, got %d", len(cached))
	}

	// Verify atomic replace: sync again with different labels
	fake2 := &fakeIssueGitHubClient{
		repoLabels: []*gh.Label{
			{Name: stringPtr("docs"), Color: stringPtr("0075ca"), Description: stringPtr("Docs desc")},
		},
	}
	err = svc.issueService.mirror.syncRepositoryLabels(context.Background(), fake2, projectID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	cached, err = svc.issueService.githubRepoLabelRepo.ListByProject(projectID)
	if err != nil {
		t.Fatalf("list cached labels after second sync: %v", err)
	}
	if len(cached) != 1 {
		t.Fatalf("expected 1 cached label after replace, got %d", len(cached))
	}
	if cached[0].Name != "docs" {
		t.Fatalf("expected docs label, got %q", cached[0].Name)
	}
}

func TestIssueSyncStateRepositoryGetOrCreate_IsAtomic(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")

	const workers = 12
	start := make(chan struct{})
	errCh := make(chan error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.syncStateRepo.GetOrCreate(project.ID)
			errCh <- err
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("get or create sync state: %v", err)
		}
	}

	var count int64
	if err := svc.db.Model(&model.IssueSyncState{}).Where("project_id = ?", project.ID).Count(&count).Error; err != nil {
		t.Fatalf("count sync state rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one sync state row, got %d", count)
	}
}
