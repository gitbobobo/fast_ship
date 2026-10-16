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
	commentsPages    map[int][][]*ghclient.IssueComment
	commentErrs      map[int]map[int]error
	timelinePages    map[int][][]*ghclient.TimelineEvent
	timelineErrs     map[int]map[int]error
	createdComment   *ghclient.IssueComment
	createCommentErr error
	updatedIssue     *ghclient.Issue
	// issuesByNumber 供 GetIssue 返回单个 issue 的远端快照（同步兜底路径 INT-105）；
	// 未命中时依次回落到 issues 列表与 updatedIssue。getIssueErr 按编号注入取数失败；
	// getIssueCalls 记录 GetIssue 调用次数，用于断言只有兜底路径会调它。
	issuesByNumber map[int]*ghclient.Issue
	getIssueErr    map[int]error
	getIssueCalls  map[int]int
	// updatedIssuesByNumber 按 issue 编号返回更新响应，供多条 issue 各自写回的
	// 批量用例使用；map 非空而编号未命中时直接 panic——写回按 GitHub 响应里的
	// issue ID 定位本地行，静默回落 updatedIssue 会让批次用例串号或造出幽灵行。
	// map 为空时回落 updatedIssue（既有单 issue 用例只配后者）。
	updatedIssuesByNumber map[int]*ghclient.Issue
	createdIssue          *ghclient.Issue
	createIssueErr        error
	createIssueCalls      []fakeCreateIssueCall
	createCommentCalls    []fakeCreateCommentCall
	updateIssueCalls      []fakeUpdateIssueCall
	pullRequests          map[int]*gh.PullRequest
	pullRequestErr        error
	onGetPullRequest      func(number int)
	// validateErr 注入 ValidateRepository 失败；onListIssues/onListTimeline 分别在
	// ListIssues / ListIssueTimeline 开头回调（用于在两轮取数之间注入写库）；
	// listIssuesSince 记录每次调用的 since 参数。
	validateErr    error
	onListIssues   func()
	onListTimeline func()
	// mu 保护 listIssuesSince 与 commentListCalls/timelineListCalls：目前只有
	// ListIssues 会被并发同步路径调用；若未来并发测试命中推送方法，其余记录
	// 字段需一并加锁。
	mu              sync.Mutex
	listIssuesSince []*time.Time
	// commentListCalls/timelineListCalls 按 issue 记录两个集合拉取接口的
	// 调用次数（命中注入错误也计入，表示请求已发出）。
	commentListCalls  map[int]int
	timelineListCalls map[int]int
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

// countCollectionCalls 返回评论/时间线拉取接口各自累计的调用次数。
func (f *fakeIssueGitHubClient) countCollectionCalls() (comments, timeline int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.commentListCalls {
		comments += n
	}
	for _, n := range f.timelineListCalls {
		timeline += n
	}
	return comments, timeline
}

func (f *fakeIssueGitHubClient) GetIssue(_ context.Context, issueNumber int) (*ghclient.Issue, error) {
	if f.getIssueCalls == nil {
		f.getIssueCalls = map[int]int{}
	}
	f.getIssueCalls[issueNumber]++
	if err := f.getIssueErr[issueNumber]; err != nil {
		return nil, err
	}
	if item, ok := f.issuesByNumber[issueNumber]; ok {
		return item, nil
	}
	for _, item := range f.issues {
		if item != nil && item.GetNumber() == issueNumber {
			return item, nil
		}
	}
	if f.updatedIssue != nil && f.updatedIssue.GetNumber() == issueNumber {
		return f.updatedIssue, nil
	}
	return nil, fmt.Errorf("fakeIssueGitHubClient: no issue configured for number %d", issueNumber)
}

// countGetIssueCalls 返回 GetIssue 累计调用次数（仅兜底路径会调它）。
func (f *fakeIssueGitHubClient) countGetIssueCalls() int {
	total := 0
	for _, n := range f.getIssueCalls {
		total += n
	}
	return total
}

func (f *fakeIssueGitHubClient) ListRepositoryLabels(context.Context, int, int) ([]*gh.Label, *gh.Response, error) {
	return f.repoLabels, &gh.Response{NextPage: 0}, f.repoLabelsErr
}

func (f *fakeIssueGitHubClient) ListIssueComments(_ context.Context, issueNumber, page, _ int) ([]*ghclient.IssueComment, *gh.Response, error) {
	f.mu.Lock()
	if f.commentListCalls == nil {
		f.commentListCalls = map[int]int{}
	}
	f.commentListCalls[issueNumber]++
	f.mu.Unlock()
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
	if f.onListTimeline != nil {
		f.onListTimeline()
	}
	f.mu.Lock()
	if f.timelineListCalls == nil {
		f.timelineListCalls = map[int]int{}
	}
	f.timelineListCalls[issueNumber]++
	f.mu.Unlock()
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
	if len(f.updatedIssuesByNumber) > 0 {
		item, ok := f.updatedIssuesByNumber[issueNumber]
		if !ok {
			panic(fmt.Sprintf("fakeIssueGitHubClient: updatedIssuesByNumber 未配置编号 %d 的更新响应", issueNumber))
		}
		return item, nil
	}
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
