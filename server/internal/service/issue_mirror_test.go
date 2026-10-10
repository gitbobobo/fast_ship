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
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
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

	result, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
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
	if issues[0].BodyHtml != "<p>App crashes on <strong>startup</strong></p>" {
		t.Fatalf("expected issue html body to be persisted, got %+v", issues[0])
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
		p.GithubTokenEncrypted = nil
	})

	_, err := svc.issueService.SyncProjectIssues(project.ID, user.ID)
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
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
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

	result, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
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
	result, err = svc.issueService.SyncProjectIssues(project.ID, project.UserID)
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

// 评论第二页拉取失败：第一页已映射的增/改与远端缺失清理都不提交，集合保持同步前数据。
func TestIssueServiceSyncProjectIssues_CommentPageFailureKeepsCollection(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		comments: map[int][]*ghclient.IssueComment{
			42: {testGitHubSyncComment(500, "old body", now), testGitHubSyncComment(599, "stale", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮：第一页改 500 并新增 501、丢弃 599，第二页失败。
	fake.commentsPages = map[int][][]*ghclient.IssueComment{
		42: {
			{testGitHubSyncComment(500, "new body", now), testGitHubSyncComment(501, "new", now)},
			{testGitHubSyncComment(502, "unreached", now)},
		},
	}
	fake.commentErrs = map[int]map[int]error{42: {2: fmt.Errorf("page 2 boom")}}

	_, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
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

	state, err := svc.syncStateRepo.Get(project.ID)
	if err != nil {
		t.Fatalf("load sync state: %v", err)
	}
	if state.Status != model.IssueSyncStatusFailed {
		t.Fatalf("expected failed sync state, got %q", state.Status)
	}
}

// 时间线第二页拉取失败：事件集合保持同步前数据；评论是独立原子边界，本轮照常提交。
func TestIssueServiceSyncProjectIssues_TimelinePageFailureKeepsCollection(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues: []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		timeline: map[int][]*ghclient.TimelineEvent{
			42: {testGitHubSyncTimelineEvent(700, "labeled", now), testGitHubSyncTimelineEvent(799, "subscribed", now)},
		},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	issueID := syncedGitHubIssueID(t, svc, project.ID)

	// 第二轮：评论集合正常更新（独立原子边界，先提交）；时间线第一页把 700 改成
	// closed 并新增 701、丢弃 799，第二页失败。
	fake.comments = map[int][]*ghclient.IssueComment{
		42: {testGitHubSyncComment(500, "committed despite timeline failure", now)},
	}
	fake.timelinePages = map[int][][]*ghclient.TimelineEvent{
		42: {
			{testGitHubSyncTimelineEvent(700, "closed", now), testGitHubSyncTimelineEvent(701, "reopened", now)},
			{testGitHubSyncTimelineEvent(702, "unreached", now)},
		},
	}
	fake.timelineErrs = map[int]map[int]error{42: {2: fmt.Errorf("page 2 boom")}}

	_, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
	if err == nil {
		t.Fatal("expected sync error on second timeline page, got nil")
	}
	if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrGitHubAPI.Code {
		t.Fatalf("expected github api error, got %v", err)
	}

	storedComments := storedCommentsByGitHubID(t, svc, issueID)
	if got := storedComments[500]; got.Body != "committed despite timeline failure" {
		t.Fatalf("expected comments committed independently of timeline failure, got %+v", storedComments)
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
}

// 远端返回空集合：GitHub 镜像行全部清掉；source='internal' 的本地评论保留。
func TestIssueServiceSyncProjectIssues_EmptyRemoteCleansMirrorKeepsInternal(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
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

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
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

	fake.comments = nil
	fake.timeline = nil

	result, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID)
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
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
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
		_, err := svc.issueService.SyncProjectIssues(project.ID, user.ID)
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first sync did not reach ListIssues")
	}

	_, err := svc.issueService.SyncProjectIssues(project.ID, user.ID)
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

	if _, err := svc.issueService.SyncProjectIssues(project.ID, user.ID); err != nil {
		t.Fatalf("sync after release should succeed: %v", err)
	}
}

// 同步失败后锁必须释放：注入评论分页失败使首轮失败，移除后第二轮成功。
func TestIssueServiceSyncProjectIssues_FailureReleasesLock(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
	})

	now := time.Now().UTC()
	fake := &fakeIssueGitHubClient{
		issues:      []*ghclient.Issue{testGitHubSyncIssue(101, 42, now)},
		commentErrs: map[int]map[int]error{42: {1: errors.New("comments down")}},
	}
	svc.issueService.newClient = func(token, owner, repo string) gitHubIssueClient {
		return fake
	}

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err == nil {
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
	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
		t.Fatalf("sync after failure should succeed: %v", err)
	}
}

// 增量同步：首轮 since 为 nil，次轮带首轮最大 updated_at 减一秒的回退；
// 远端返回空集时不推进也不清空 LastIssueUpdatedAt。
func TestIssueServiceSyncProjectIssues_IncrementalSince(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "gh-token")
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

	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
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
	if _, err := svc.issueService.SyncProjectIssues(project.ID, project.UserID); err != nil {
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

// SyncAll 跳过未配置 GitHub 的项目，单项目失败不中断后续项目。
func TestIssueMirrorSyncAll_SkipsUnconfiguredAndContinuesAfterFailure(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")

	now := time.Now().UTC()
	unconfigured := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = nil
	})
	healthy := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "tok-ok")
		p.CreatedAt = now.Add(-time.Minute)
	})
	// ListAll 按 created_at DESC，失败项目排最前先同步。
	failing := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubTokenEncrypted = encryptTestToken(t, svc.cfg, "tok-fail")
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
