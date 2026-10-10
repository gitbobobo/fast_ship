package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
)

type fakeIssueGitHubClient struct {
	issues        []*ghclient.Issue
	repoLabels    []*gh.Label
	repoLabelsErr error
	comments      map[int][]*ghclient.IssueComment
	timeline      map[int][]*ghclient.TimelineEvent
	// 分页模式：commentsPages/timelinePages 按页返回并推导 NextPage；
	// commentErrs/timelineErrs 按 (issueNumber, page) 注入拉取失败。
	commentsPages      map[int][][]*ghclient.IssueComment
	commentErrs        map[int]map[int]error
	timelinePages      map[int][][]*ghclient.TimelineEvent
	timelineErrs       map[int]map[int]error
	createdComment     *ghclient.IssueComment
	createCommentErr   error
	updatedIssue       *ghclient.Issue
	createdIssue       *ghclient.Issue
	createIssueErr     error
	createIssueCalls   []fakeCreateIssueCall
	createCommentCalls []fakeCreateCommentCall
	updateIssueCalls   []fakeUpdateIssueCall
	pullRequests       map[int]*gh.PullRequest
	pullRequestErr     error
	onGetPullRequest   func(number int)
	// validateErr 注入 ValidateRepository 失败；onListIssues 在 ListIssues
	// 开头回调（可阻塞同步）；listIssuesSince 记录每次调用的 since 参数。
	validateErr  error
	onListIssues func()
	// mu 仅保护 listIssuesSince：目前只有 ListIssues 会被并发同步路径调用；
	// 若未来并发测试命中推送方法，其余记录字段需一并加锁。
	mu              sync.Mutex
	listIssuesSince []*time.Time
}

type fakeCreateIssueCall struct {
	Title string
	Body  string
}

type fakeCreateCommentCall struct {
	IssueNumber int
	Body        string
}

type fakeUpdateIssueCall struct {
	IssueNumber int
	Title       string
	Body        string
	State       string
	StateReason string
	Labels      []string
}

func (f *fakeIssueGitHubClient) ValidateRepository(context.Context) error {
	return f.validateErr
}

func (f *fakeIssueGitHubClient) ListIssues(_ context.Context, _ string, since *time.Time, _, _ int) ([]*ghclient.Issue, *gh.Response, error) {
	if f.onListIssues != nil {
		f.onListIssues()
	}
	f.mu.Lock()
	var recorded *time.Time
	if since != nil {
		copied := *since
		recorded = &copied
	}
	f.listIssuesSince = append(f.listIssuesSince, recorded)
	f.mu.Unlock()
	return f.issues, &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) recordedSinceCalls() []*time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*time.Time(nil), f.listIssuesSince...)
}

func (f *fakeIssueGitHubClient) ListRepositoryLabels(context.Context, int, int) ([]*gh.Label, *gh.Response, error) {
	return f.repoLabels, &gh.Response{NextPage: 0}, f.repoLabelsErr
}

func (f *fakeIssueGitHubClient) ListIssueComments(_ context.Context, issueNumber, page, _ int) ([]*ghclient.IssueComment, *gh.Response, error) {
	if err := f.commentErrs[issueNumber][page]; err != nil {
		return nil, nil, err
	}
	if pages, ok := f.commentsPages[issueNumber]; ok {
		var items []*ghclient.IssueComment
		if page >= 1 && page <= len(pages) {
			items = pages[page-1]
		}
		next := 0
		if page < len(pages) {
			next = page + 1
		}
		return items, &gh.Response{NextPage: next}, nil
	}
	return f.comments[issueNumber], &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) ListIssueTimeline(_ context.Context, issueNumber, page, _ int) ([]*ghclient.TimelineEvent, *gh.Response, error) {
	if err := f.timelineErrs[issueNumber][page]; err != nil {
		return nil, nil, err
	}
	if pages, ok := f.timelinePages[issueNumber]; ok {
		var items []*ghclient.TimelineEvent
		if page >= 1 && page <= len(pages) {
			items = pages[page-1]
		}
		next := 0
		if page < len(pages) {
			next = page + 1
		}
		return items, &gh.Response{NextPage: next}, nil
	}
	return f.timeline[issueNumber], &gh.Response{NextPage: 0}, nil
}

func (f *fakeIssueGitHubClient) CreateIssueComment(_ context.Context, issueNumber int, body string) (*ghclient.IssueComment, error) {
	f.createCommentCalls = append(f.createCommentCalls, fakeCreateCommentCall{
		IssueNumber: issueNumber,
		Body:        body,
	})
	if f.createCommentErr != nil {
		return nil, f.createCommentErr
	}
	return f.createdComment, nil
}

func (f *fakeIssueGitHubClient) UpdateIssue(_ context.Context, issueNumber int, req ghclient.UpdateIssueRequest) (*ghclient.Issue, error) {
	call := fakeUpdateIssueCall{IssueNumber: issueNumber}
	if req.Title != nil {
		call.Title = *req.Title
	}
	if req.Body != nil {
		call.Body = *req.Body
	}
	if req.State != nil {
		call.State = *req.State
	}
	if req.StateReason != nil {
		call.StateReason = *req.StateReason
	}
	if req.Labels != nil {
		call.Labels = append([]string(nil), (*req.Labels)...)
	}
	f.updateIssueCalls = append(f.updateIssueCalls, call)
	return f.updatedIssue, nil
}

func (f *fakeIssueGitHubClient) CreateIssue(_ context.Context, title, body string) (*ghclient.Issue, error) {
	f.createIssueCalls = append(f.createIssueCalls, fakeCreateIssueCall{
		Title: title,
		Body:  body,
	})
	return f.createdIssue, f.createIssueErr
}

func (f *fakeIssueGitHubClient) GetPullRequest(_ context.Context, number int) (*gh.PullRequest, error) {
	if f.onGetPullRequest != nil {
		f.onGetPullRequest(number)
	}
	if f.pullRequestErr != nil {
		return nil, f.pullRequestErr
	}
	pr, ok := f.pullRequests[number]
	if !ok {
		return nil, fmt.Errorf("pull request %d not found", number)
	}
	return pr, nil
}
