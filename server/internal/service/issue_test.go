package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
)

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
	if issues[0].Title != "Crash on launch" || issues[0].Labels[0].Name != "bug" {
		t.Fatalf("unexpected issue payload: %+v", issues[0])
	}
	if issues[0].BodyHTML != "<p>App crashes on <strong>startup</strong></p>" {
		t.Fatalf("expected issue html body to be persisted, got %+v", issues[0])
	}

	comments, total, err := svc.issueService.ListComments(issues[0].ID, project.UserID, 1, 20)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if total != 1 || len(comments) != 1 || comments[0].Author.Login != "bob" {
		t.Fatalf("unexpected comments payload: %+v", comments)
	}
	if comments[0].BodyHTML == "" {
		t.Fatalf("expected comment html body to be persisted, got %+v", comments[0])
	}

	events, total, err := svc.issueService.ListTimeline(issues[0].ID, project.UserID, 1, 20)
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

type fakeIssueGitHubClient struct {
	issues   []*ghclient.Issue
	comments map[int][]*ghclient.IssueComment
	timeline map[int][]*ghclient.TimelineEvent
}

func (f *fakeIssueGitHubClient) ValidateRepository(context.Context) error {
	return nil
}

func (f *fakeIssueGitHubClient) ListIssues(context.Context, string, *time.Time, int, int) ([]*ghclient.Issue, *gh.Response, error) {
	return f.issues, &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) ListIssueComments(_ context.Context, issueNumber, _, _ int) ([]*ghclient.IssueComment, *gh.Response, error) {
	return f.comments[issueNumber], &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) ListIssueTimeline(_ context.Context, issueNumber, _, _ int) ([]*ghclient.TimelineEvent, *gh.Response, error) {
	return f.timeline[issueNumber], &gh.Response{NextPage: 0}, nil
}

func intPtr(v int) *int {
	return &v
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
