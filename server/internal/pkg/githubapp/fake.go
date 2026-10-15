package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fake PullRequestGitHub 的内存实现：读接口返回入站时存下的 PR 数据（SetPR），
// diff 固定短字符串；写接口记账（Calls）供测试断言。注释/check 的 ID 按序号递增。
// webhook 事务期间 GitHub 调用数为 0 的断言靠 CallCounts。

type Fake struct {
	mu             sync.Mutex
	prs            map[string]map[int]*PullRequest  // repo -> number -> PR
	comments       map[string]map[int][]fakeComment // repo -> number -> issue comments
	reviews        map[string][]fakeComment         // repo -> review comments
	checks         map[string]fakeCheck             // repo -> latest check per id
	nextID         int64
	diffText       string
	forbidCheck    bool // 按模型 CreateCheckRun 返回 403（退回合并 check）
	forbidAllCheck bool // 全部 Create/UpdateCheckRun 返回 403（App 无 Checks 权限）

	Calls []FakeCall
}

type fakeComment struct {
	ID        int64
	User      string
	Body      string
	CreatedAt time.Time
}

type fakeCheck struct {
	ID      int64
	Name    string
	HeadSHA string
	Update  CheckUpdate
}

// FakeCall 一次写调用的记账（读调用也记，供「事务内调用数为 0」断言）。
type FakeCall struct {
	Method string // GetPullRequest / CreateIssueComment / ...
	Repo   string
	Number int
	Detail string // 附加信息（review event、check 状态等）
}

func NewFake() *Fake {
	return &Fake{
		prs:      map[string]map[int]*PullRequest{},
		comments: map[string]map[int][]fakeComment{},
		reviews:  map[string][]fakeComment{},
		checks:   map[string]fakeCheck{},
		diffText: "diff --git a/main.go b/main.go\n+func main() {}\n",
		nextID:   1000,
	}
}

func (f *Fake) record(method, repo string, number int, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, FakeCall{Method: method, Repo: repo, Number: number, Detail: detail})
}

// CallCounts 各方法的调用次数快照。
func (f *Fake) CallCounts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, c := range f.Calls {
		out[c.Method]++
	}
	return out
}

// TotalCalls 累计调用次数。
func (f *Fake) TotalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// SetPR 入站 PR 数据（GetPullRequest 读这份）。
func (f *Fake) SetPR(repo string, number int, pr *PullRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prs[repo] == nil {
		f.prs[repo] = map[int]*PullRequest{}
	}
	pr.RepoFullName = repo
	pr.Number = number
	f.prs[repo][number] = pr
}

// SetDiff 覆盖固定 diff 文本。
func (f *Fake) SetDiff(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffText = text
}

// ForbidChecks 让按模型的 CreateCheckRun 返回 403（验证退回合并 check）。
func (f *Fake) ForbidChecks(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forbidCheck = v
}

// ForbidAllChecks 让全部 check 写接口返回 403（App 未授权 Checks）。
func (f *Fake) ForbidAllChecks(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forbidAllCheck = v
}

// Comments 当前 issue 评论快照（repo+pr）。
func (f *Fake) Comments(repo string, number int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.comments[repo][number] {
		out = append(out, c.Body)
	}
	return out
}

// LastCommentBody 最近一次写入的 issue 评论正文。
func (f *Fake) LastCommentBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *fakeComment
	for _, byNumber := range f.comments {
		for _, list := range byNumber {
			for i := range list {
				if best == nil || list[i].ID > best.ID {
					best = &list[i]
				}
			}
		}
	}
	if best == nil {
		return ""
	}
	return best.Body
}

// CheckRuns 该仓库全部 check（按 id 排序）。
func (f *Fake) CheckRuns(repo string) []fakeCheck {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCheck
	for _, c := range f.checks {
		if c.Name != "" {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ReviewEvents 提交过的 review 事件列表。
func (f *Fake) ReviewEvents(repo string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.Calls {
		if c.Method == "SubmitReview" && c.Repo == repo {
			out = append(out, c.Detail)
		}
	}
	return out
}

func (f *Fake) GetPullRequest(ctx context.Context, installationID int64, repoFullName string, number int) (*PullRequest, error) {
	f.record("GetPullRequest", repoFullName, number, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	pr, ok := f.prs[repoFullName][number]
	if !ok {
		return nil, fmt.Errorf("githubapp fake: 未登记的 PR %s#%d", repoFullName, number)
	}
	cp := *pr
	return &cp, nil
}

func (f *Fake) GetPullRequestDiff(ctx context.Context, installationID int64, repoFullName string, number int) (string, error) {
	f.record("GetPullRequestDiff", repoFullName, number, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.diffText, nil
}

func (f *Fake) ListReviewComments(ctx context.Context, installationID int64, repoFullName string, number int) ([]ReviewComment, error) {
	f.record("ListReviewComments", repoFullName, number, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.reviews[repoFullName]
	out := make([]ReviewComment, 0, len(list))
	for _, c := range list {
		out = append(out, ReviewComment{ID: c.ID, User: c.User, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	return out, nil
}

func (f *Fake) FindIssueCommentByMarker(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (int64, bool, error) {
	f.record("FindIssueCommentByMarker", repoFullName, number, marker)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.comments[repoFullName][number] {
		if strings.Contains(c.Body, marker) {
			return c.ID, true, nil
		}
	}
	return 0, false, nil
}

func (f *Fake) CreateIssueComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (int64, error) {
	f.record("CreateIssueComment", repoFullName, number, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.comments[repoFullName] == nil {
		f.comments[repoFullName] = map[int][]fakeComment{}
	}
	f.nextID++
	id := f.nextID
	f.comments[repoFullName][number] = append(f.comments[repoFullName][number],
		fakeComment{ID: id, User: "fast-ship[bot]", Body: body, CreatedAt: time.Now().UTC()})
	return id, nil
}

func (f *Fake) UpdateIssueComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error {
	f.record("UpdateIssueComment", repoFullName, int(commentID%100000), "")
	f.mu.Lock()
	defer f.mu.Unlock()
	for number, list := range f.comments[repoFullName] {
		for i := range list {
			if list[i].ID == commentID {
				f.comments[repoFullName][number][i].Body = body
				return nil
			}
		}
	}
	return &StatusError{StatusCode: 404, Method: "PATCH", URL: "issues/comments", Body: "not found"}
}

func (f *Fake) CreateCheckRun(ctx context.Context, installationID int64, repoFullName, name, headSHA, externalID string) (int64, error) {
	f.record("CreateCheckRun", repoFullName, 0, name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forbidAllCheck {
		return 0, ErrForbidden
	}
	// ForbidChecks 只拒绝按模型的 check 名；合并 check（退回形态）保持可用，
	// 与「403 只影响 Checks 按模型粒度」的真实场景一致。
	if f.forbidCheck && name != "Fast Ship review" {
		return 0, ErrForbidden
	}
	f.nextID++
	id := f.nextID
	f.checks[fmt.Sprintf("%s/%d", repoFullName, id)] = fakeCheck{ID: id, Name: name, HeadSHA: headSHA}
	return id, nil
}

func (f *Fake) UpdateCheckRun(ctx context.Context, installationID int64, repoFullName string, checkRunID int64, in CheckUpdate) error {
	f.record("UpdateCheckRun", repoFullName, int(checkRunID%100000), fmt.Sprintf("%s/%s/%s", in.Status, in.Conclusion, in.Title))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forbidAllCheck {
		return ErrForbidden
	}
	key := fmt.Sprintf("%s/%d", repoFullName, checkRunID)
	c, ok := f.checks[key]
	if !ok {
		return fmt.Errorf("githubapp fake: check 不存在 repo=%s id=%d", repoFullName, checkRunID)
	}
	c.Update = in
	f.checks[key] = c
	return nil
}

func (f *Fake) SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in ReviewInput) error {
	f.record("SubmitReview", repoFullName, number, in.Event)
	return nil
}

// SeedFakeFromWebhook 把 webhook payload 登记进 fake，让随后的 GetPullRequest
// 读到同一份 PR（本地 simulate 脚本不另调 SetPR）。解析失败静默忽略。
func SeedFakeFromWebhook(f *Fake, body []byte) {
	if f == nil || len(body) == 0 {
		return
	}
	var p struct {
		Number      int `json:"number"`
		PullRequest struct {
			HTMLURL string  `json:"html_url"`
			Title   string  `json:"title"`
			Body    *string `json:"body"`
			Head    struct {
				SHA string `json:"sha"`
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &p) != nil || p.Number == 0 || p.Repository.FullName == "" {
		return
	}
	bodyText := ""
	if p.PullRequest.Body != nil {
		bodyText = *p.PullRequest.Body
	}
	f.SetPR(p.Repository.FullName, p.Number, &PullRequest{
		Title:   p.PullRequest.Title,
		Body:    bodyText,
		HTMLURL: p.PullRequest.HTMLURL,
		HeadSHA: p.PullRequest.Head.SHA,
		HeadRef: p.PullRequest.Head.Ref,
		BaseRef: p.PullRequest.Base.Ref,
	})
}
