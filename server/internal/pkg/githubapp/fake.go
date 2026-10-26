package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// Fake githubapi.GitHubClient 的内存实现：读接口返回入站时存下的 PR 数据
// （SetPR），diff 固定短字符串；写接口记账（Calls）供测试断言。评论/状态/review 的
// ID 按序号递增。webhook 事务期间 GitHub 调用数为 0 的断言靠 CallCounts。
// 仓库键按小写归一（真实 GitHub API 大小写不敏感）。

// 编译期钉住 Fake 满足 seam 契约。
var _ githubapi.GitHubClient = (*Fake)(nil)

// HookDeliveries 投递记录访问面同 Fake 一并实现（审查调度对账的测试与
// debug 自测走同一份假数据）。
var _ HookDeliveries = (*Fake)(nil)

type Fake struct {
	mu               sync.Mutex
	prs              map[string]map[int]*githubapi.PullRequest // repo -> number -> PR
	comments         map[string]map[int][]fakeComment          // repo -> number -> issue comments
	reviews          map[string][]fakeComment                  // repo -> review comments
	statuses         []fakeStatus                              // CreateCommitStatus 记录（提交序）
	reviewInputs     []githubapi.ReviewInput                   // SubmitReview 的完整入参
	submittedReviews []githubapi.ReviewSummary                 // 已提交 review（对账用）
	repoErrs         map[string]error                          // repo -> GetRepository 注入的错误（nil=可访问）
	prErrs           map[string]error                          // repo -> GetPullRequest 注入的错误（nil=按登记表返回）
	treePaths        map[string]map[string]map[string]bool     // repo -> ref -> 路径集合（SetTreeFiles 建立；无 repo 索引=不建模树）
	treeErrs         map[string]error                          // repo -> PathExistsAtRef 注入错误
	treeHook         func(repo, ref, path string)              // PathExistsAtRef 前置钩子（测试注入并发副作用）
	prCalls          int                                       // GetPullRequest 已调用次数（prHook 判定用）
	prHook           func(call int)                            // GetPullRequest 前置钩子，call 为本次序号（测试注入并发副作用）
	hookDeliveries   []HookDelivery                            // 投递记录（SetHookDeliveries 覆盖，重投时追加行）
	hookPayloads     map[int64]json.RawMessage                 // delivery id -> 投递 payload（SetHookDeliveryPayload 登记）
	redeliveredIDs   []int64                                   // 已触发重投的 delivery id（调用序）
	redeliverErr     error                                     // SetRedeliverError 注入的重投错误（非 nil 时不记账）
	nextID           int64
	diffText         string
	diffErr          error

	// InstallationID ResolveInstallation 返回的固定安装 id（默认 1）；
	// 置 <=0 表示不支持解析（返回错误）。
	InstallationID int64

	Calls []FakeCall
}

// fakeRepoKey 仓库 map 键统一小写。
func fakeRepoKey(repo string) string { return strings.ToLower(repo) }

type fakeComment struct {
	ID        int64
	User      string
	Body      string
	Path      string // 行内评论的文件路径（issue 评论为空）
	CreatedAt time.Time
}

// fakeStatus 一次 commit status 写入（INT-135 审查状态输出断言用）。
type fakeStatus struct {
	SHA         string
	State       string
	Context     string
	Description string
	TargetURL   string
}

// FakeCall 一次写调用的记账（读调用也记，供「事务内调用数为 0」断言）。
type FakeCall struct {
	Method string // GetPullRequest / CreatePullRequestComment / ...
	Repo   string
	Number int
	Detail string // 附加信息（review event、commit status 状态等）
}

func NewFake() *Fake {
	return &Fake{
		prs:            map[string]map[int]*githubapi.PullRequest{},
		comments:       map[string]map[int][]fakeComment{},
		reviews:        map[string][]fakeComment{},
		repoErrs:       map[string]error{},
		prErrs:         map[string]error{},
		treePaths:      map[string]map[string]map[string]bool{},
		treeErrs:       map[string]error{},
		hookPayloads:   map[int64]json.RawMessage{},
		diffText:       "diff --git a/main.go b/main.go\n+func main() {}\n",
		nextID:         1000,
		InstallationID: 1,
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

// SetPR 入站 PR 数据（GetPullRequest 读这份）。State 未指定时按 open 处理：
// 真实 GitHub 的 PR 一定带 state，登记的 PR 默认就是「还开着」——需要关闭/
// 合并语义的用例显式写 State="closed"。
func (f *Fake) SetPR(repo string, number int, pr *githubapi.PullRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeRepoKey(repo)
	if f.prs[key] == nil {
		f.prs[key] = map[int]*githubapi.PullRequest{}
	}
	pr.RepoFullName = repo
	pr.Number = number
	if pr.State == "" {
		pr.State = "open"
	}
	f.prs[key][number] = pr
}

// SetDiffError 让 GetPullRequestDiff 返回指定错误（截断/平台失败注入）。
func (f *Fake) SetDiffError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffErr = err
}

// SetDiff 覆盖固定 diff 文本。
func (f *Fake) SetDiff(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffText = text
}

// SetTreeFiles 为仓库某 ref 建立树路径索引（INT-169 文件归属校验）：索引按
// (repo, ref) 建——同仓不同 ref 各自一份，借此发现实现误用默认分支/最新
// head 而非运行固定 head_sha；没建索引的仓库恒存在（存量用例不必登记），
// 建过索引的仓库按 ref 严格判定。同 (repo, ref) 重复调用整组替换。
func (f *Fake) SetTreeFiles(repo, ref string, paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeRepoKey(repo)
	if f.treePaths[key] == nil {
		f.treePaths[key] = map[string]map[string]bool{}
	}
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	f.treePaths[key][ref] = set
}

// SetTreeError 让该仓库的 PathExistsAtRef 返回指定错误（网络/5xx 注入）。
func (f *Fake) SetTreeError(repo string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.treeErrs[fakeRepoKey(repo)] = err
}

// SetPRHook 注册 GetPullRequest 前置钩子：按调用序号触发（测试中在 pr2
// 复核等窗口注入并发副作用）。传 nil 清除。
func (f *Fake) SetPRHook(fn func(call int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prHook = fn
}

// SetTreeHook 注册 PathExistsAtRef 前置钩子：在查询判定前执行（测试中模拟
// 校验期间的外界状态变化，如并发提交完成运行）。nil 清除。
func (f *Fake) SetTreeHook(fn func(repo, ref, path string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.treeHook = fn
}

// PathExistsAtRef 按 SetTreeFiles 索引判定；未建索引的仓库返回 true。
func (f *Fake) PathExistsAtRef(ctx context.Context, installationID int64, repoFullName, path, ref string) (bool, error) {
	f.record("PathExistsAtRef", repoFullName, 0, path+"@"+ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.treeHook != nil {
		f.treeHook(repoFullName, ref, path)
	}
	if err := f.treeErrs[fakeRepoKey(repoFullName)]; err != nil {
		return false, err
	}
	set, indexed := f.treePaths[fakeRepoKey(repoFullName)]
	if !indexed {
		return true, nil
	}
	return set[ref][path], nil
}

// Comments 当前 issue 评论快照（repo+pr）。
func (f *Fake) Comments(repo string, number int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.comments[fakeRepoKey(repo)][number] {
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

// ReviewEvents 提交过的 review 事件列表。
func (f *Fake) ReviewEvents(repo string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.Calls {
		if c.Method == "SubmitReview" && strings.EqualFold(c.Repo, repo) {
			out = append(out, c.Detail)
		}
	}
	return out
}

// ReviewInputs 提交过的 review 完整入参（供行内评论断言）。
func (f *Fake) ReviewInputs() []githubapi.ReviewInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]githubapi.ReviewInput, len(f.reviewInputs))
	copy(out, f.reviewInputs)
	return out
}

// CommitStatuses 按提交序的全部 commit status 记录。
func (f *Fake) CommitStatuses() []fakeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeStatus, len(f.statuses))
	copy(out, f.statuses)
	return out
}

// StatusesForContext 过滤某 context（代码审查/视觉审查）的状态序列。
func (f *Fake) StatusesForContext(statusContext string) []fakeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeStatus
	for _, s := range f.statuses {
		if s.Context == statusContext {
			out = append(out, s)
		}
	}
	return out
}

// AddIssueComment 种子一条 PR 会话评论（历史上下文测试用；ID 自增）。
func (f *Fake) AddIssueComment(repo string, number int, user, body string, createdAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeRepoKey(repo)
	if f.comments[key] == nil {
		f.comments[key] = map[int][]fakeComment{}
	}
	f.nextID++
	f.comments[key][number] = append(f.comments[key][number],
		fakeComment{ID: f.nextID, User: user, Body: body, CreatedAt: createdAt})
}

// AddReviewComment 种子一条行内评论（历史上下文测试用；ID 自增）。
func (f *Fake) AddReviewComment(repo string, user, path, body string, createdAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	key := fakeRepoKey(repo)
	f.reviews[key] = append(f.reviews[key],
		fakeComment{ID: f.nextID, User: user, Path: path, Body: body, CreatedAt: createdAt})
}

// SetPRError 注入 PR 级读取错误：err 非 nil 时该 repo 的 GetPullRequest 一律
// 返回它（如 &githubapi.StatusError{StatusCode: 403} 模拟凭证被拒）；传 nil
// 恢复按登记表返回。
func (f *Fake) SetPRError(repo string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prErrs[fakeRepoKey(repo)] = err
}

func (f *Fake) GetPullRequest(ctx context.Context, installationID int64, repoFullName string, number int) (*githubapi.PullRequest, error) {
	f.record("GetPullRequest", repoFullName, number, "")
	f.mu.Lock()
	f.prCalls++
	hook, call := f.prHook, f.prCalls
	f.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.prErrs[fakeRepoKey(repoFullName)]; err != nil {
		return nil, err
	}
	pr, ok := f.prs[fakeRepoKey(repoFullName)][number]
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
	if f.diffErr != nil {
		return "", f.diffErr
	}
	return f.diffText, nil
}

func (f *Fake) ListReviewComments(ctx context.Context, installationID int64, repoFullName string, number int) ([]githubapi.ReviewComment, error) {
	f.record("ListReviewComments", repoFullName, number, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.reviews[fakeRepoKey(repoFullName)]
	out := make([]githubapi.ReviewComment, 0, len(list))
	for _, c := range list {
		out = append(out, githubapi.ReviewComment{ID: c.ID, User: c.User, Body: c.Body, Path: c.Path, CreatedAt: c.CreatedAt})
	}
	return out, nil
}

func (f *Fake) ListIssueCommentsForPR(ctx context.Context, installationID int64, repoFullName string, number int) ([]githubapi.IssueComment, error) {
	f.record("ListIssueCommentsForPR", repoFullName, number, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.comments[fakeRepoKey(repoFullName)][number]
	out := make([]githubapi.IssueComment, 0, len(list))
	for _, c := range list {
		out = append(out, githubapi.IssueComment{ID: c.ID, User: c.User, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	return out, nil
}

func (f *Fake) FindPullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, marker string) (int64, bool, error) {
	f.record("FindPullRequestComment", repoFullName, number, marker)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.comments[fakeRepoKey(repoFullName)][number] {
		if !strings.HasSuffix(c.User, "[bot]") || !strings.HasPrefix(strings.TrimSpace(c.Body), marker) {
			continue
		}
		return c.ID, true, nil
	}
	return 0, false, nil
}

// SeedIssueComment 落一条任意作者的 issue 评论（测试「用户引用 marker」用）。
func (f *Fake) SeedIssueComment(repoFullName string, number int, user, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeRepoKey(repoFullName)
	if f.comments[key] == nil {
		f.comments[key] = map[int][]fakeComment{}
	}
	f.nextID++
	f.comments[key][number] = append(f.comments[key][number],
		fakeComment{ID: f.nextID, User: user, Body: body, CreatedAt: time.Now().UTC()})
}

func (f *Fake) CreatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, number int, body string) (int64, error) {
	f.record("CreatePullRequestComment", repoFullName, number, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeRepoKey(repoFullName)
	if f.comments[key] == nil {
		f.comments[key] = map[int][]fakeComment{}
	}
	f.nextID++
	id := f.nextID
	f.comments[key][number] = append(f.comments[key][number],
		fakeComment{ID: id, User: "fast-ship[bot]", Body: body, CreatedAt: time.Now().UTC()})
	return id, nil
}

func (f *Fake) UpdatePullRequestComment(ctx context.Context, installationID int64, repoFullName string, commentID int64, body string) error {
	f.record("UpdatePullRequestComment", repoFullName, int(commentID%100000), "")
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeRepoKey(repoFullName)
	for number, list := range f.comments[key] {
		for i := range list {
			if list[i].ID == commentID {
				f.comments[key][number][i].Body = body
				return nil
			}
		}
	}
	return &githubapi.StatusError{StatusCode: 404, Method: "PATCH", URL: "issues/comments", Body: "not found"}
}

func (f *Fake) SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in githubapi.ReviewInput) (int64, error) {
	f.record("SubmitReview", repoFullName, number, in.Event)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewInputs = append(f.reviewInputs, in)
	f.nextID++
	f.submittedReviews = append(f.submittedReviews, githubapi.ReviewSummary{
		ID: f.nextID, CommitID: in.CommitID, Body: in.Body, State: in.Event, CreatedAt: time.Now().UTC(),
	})
	return f.nextID, nil
}

func (f *Fake) ListReviews(_ context.Context, _ int64, _ string, _ int) ([]githubapi.ReviewSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]githubapi.ReviewSummary, len(f.submittedReviews))
	copy(out, f.submittedReviews)
	return out, nil
}

func (f *Fake) CreateCommitStatus(ctx context.Context, installationID int64, repoFullName, sha, state, statusContext, description, targetURL string) error {
	f.record("CreateCommitStatus", repoFullName, 0, statusContext+":"+state)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, fakeStatus{
		SHA: sha, State: state, Context: statusContext, Description: description, TargetURL: targetURL,
	})
	return nil
}

// SetRepoError 注入仓库级检查结果：err 非 nil 时 GetRepository 返回它
// （如 &githubapi.StatusError{StatusCode: 404} 模拟 App 未装到该仓库）。
func (f *Fake) SetRepoError(repo string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repoErrs[fakeRepoKey(repo)] = err
}

func (f *Fake) GetRepository(ctx context.Context, installationID int64, repoFullName string) error {
	f.record("GetRepository", repoFullName, 0, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repoErrs[fakeRepoKey(repoFullName)]
}

// ResolveInstallation 返回配置的固定安装 id；<=0 表示不支持解析（调用方
// 不得把 0 当安装 id 继续，应与真实实现同样报错）。
func (f *Fake) ResolveInstallation(ctx context.Context, repoFullName string) (int64, error) {
	f.record("ResolveInstallation", repoFullName, 0, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.InstallationID <= 0 {
		return 0, fmt.Errorf("githubapp fake: 未配置安装 id，无法解析")
	}
	return f.InstallationID, nil
}

// Bearer fake 没有真实凭证：恒返回空串（匿名下载即可）。
func (f *Fake) Bearer(context.Context, int64) (string, error) { return "", nil }

// SetHookDeliveries 覆盖返回的投递记录（审查调度对账测试的种子数据）。
func (f *Fake) SetHookDeliveries(items []HookDelivery) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hookDeliveries = append([]HookDelivery(nil), items...)
}

// SetHookDeliveryPayload 登记单次投递的请求 payload（GetHookDelivery 读），
// 对账闸门据此确定投递指向哪个 PR。未登记的投递返回错误，走调用方的降级
// 路径（照常重投）——与真实 GitHub 一定有详情不同，这里的「没有」就是测试
// 注入降级场景的开关。
func (f *Fake) SetHookDeliveryPayload(id int64, payload json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hookPayloads[id] = append(json.RawMessage(nil), payload...)
}

// GetHookDelivery 返回单次投递详情：元数据取自投递记录行，payload 取登记表；
// 投递行或 payload 任一未登记即报错。
func (f *Fake) GetHookDelivery(_ context.Context, deliveryID int64) (HookDeliveryDetail, error) {
	f.record("GetHookDelivery", "", 0, fmt.Sprintf("delivery:%d", deliveryID))
	f.mu.Lock()
	defer f.mu.Unlock()
	payload, ok := f.hookPayloads[deliveryID]
	if !ok {
		return HookDeliveryDetail{}, fmt.Errorf("githubapp fake: 未登记的投递 payload %d", deliveryID)
	}
	for _, d := range f.hookDeliveries {
		if d.ID != deliveryID {
			continue
		}
		return HookDeliveryDetail{
			ID: d.ID, GUID: d.GUID, Event: d.Event, Action: d.Action,
			Payload: append(json.RawMessage(nil), payload...),
		}, nil
	}
	return HookDeliveryDetail{}, fmt.Errorf("githubapp fake: 未登记的投递 %d", deliveryID)
}

// RedeliveredIDs 按调用序返回已触发重投的 delivery id。
func (f *Fake) RedeliveredIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int64, len(f.redeliveredIDs))
	copy(out, f.redeliveredIDs)
	return out
}

// SetRedeliverError 注入 RedeliverHookDelivery 的错误：非 nil 时重投请求直接
// 返回它，不追加投递行、不记 redeliveredIDs（模拟 GitHub 限流/故障，调用
// 计数照常记账，供「只试了一次」断言）。
func (f *Fake) SetRedeliverError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redeliverErr = err
}

// ListHookDeliveries 返回 DeliveredAt >= since 的记录（含失败的）。
func (f *Fake) ListHookDeliveries(_ context.Context, since time.Time) ([]HookDelivery, error) {
	f.record("ListHookDeliveries", "", 0, "")
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []HookDelivery
	for _, d := range f.hookDeliveries {
		if !d.DeliveredAt.Before(since) {
			out = append(out, d)
		}
	}
	return out, nil
}

// RedeliverHookDelivery 记账并像真实 GitHub 那样追加一条同 guid、
// Redelivery=true 的失败投递行（id 递增、DeliveredAt 用当前时间），让下一次
// 列表读到的 Attempts 推进一档。
func (f *Fake) RedeliverHookDelivery(_ context.Context, deliveryID int64) error {
	f.record("RedeliverHookDelivery", "", 0, fmt.Sprintf("delivery:%d", deliveryID))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redeliverErr != nil {
		return f.redeliverErr
	}
	for _, d := range f.hookDeliveries {
		if d.ID != deliveryID {
			continue
		}
		f.nextID++
		f.redeliveredIDs = append(f.redeliveredIDs, deliveryID)
		f.hookDeliveries = append(f.hookDeliveries, HookDelivery{
			ID: f.nextID, GUID: d.GUID, Event: d.Event, Action: d.Action,
			StatusCode: 500, Redelivery: true, DeliveredAt: time.Now().UTC(),
		})
		return nil
	}
	return fmt.Errorf("githubapp fake: 未登记的投递 %d", deliveryID)
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
	f.SetPR(p.Repository.FullName, p.Number, &githubapi.PullRequest{
		Title:   p.PullRequest.Title,
		Body:    bodyText,
		HTMLURL: p.PullRequest.HTMLURL,
		HeadSHA: p.PullRequest.Head.SHA,
		HeadRef: p.PullRequest.Head.Ref,
		BaseRef: p.PullRequest.Base.Ref,
	})
}
