package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Forgejo 适配器（INT-167 任务 3）httptest 定向测试：每方法 happy path +
// 关键字段映射 + 404→IsNotFound + status description 255 rune 截断 + Bearer。
// 适配器直连 httptest（不经过 service 侧的 SSRF client 构建），这里验证的是
// 请求形状与响应映射。

// newForgejoTestClient 起 httptest 服务并返回适配器；handler 记录最近一次
// 请求供断言（method/path/Authorization/body）。
type forgejoServeLog struct {
	method string
	path   string
	auth   string
	body   []byte
}

func newForgejoTestClient(t *testing.T, handler http.HandlerFunc) (*ForgejoReviewClient, *[]forgejoServeLog) {
	t.Helper()
	logs := &[]forgejoServeLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		*logs = append(*logs, forgejoServeLog{method: r.Method, path: r.URL.RequestURI(), auth: r.Header.Get("Authorization"), body: body})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewForgejoReviewClient(srv.Client(), srv.URL+"/api/v1", "pat-123"), logs
}

func lastServeLog(t *testing.T, logs *[]forgejoServeLog) forgejoServeLog {
	t.Helper()
	if n := len(*logs); n > 0 {
		return (*logs)[n-1]
	}
	t.Fatal("没有请求发出")
	return forgejoServeLog{}
}

func jsonBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("请求体不是 JSON: %v (%s)", err, raw)
	}
	return out
}

func TestForgejoClient_GetPullRequest(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"number": 7, "title": "T", "body": "B", "state": "open",
			"html_url": "https://forge.example.com/o/r/pulls/7",
			"merged": false, "merged_at": null,
			"closed_at": "2026-01-02T03:04:05Z",
			"head": {"ref": "feat", "sha": "abc", "repo": {"full_name": "o/r", "fork": false}},
			"base": {"ref": "main", "repo": {"full_name": "o/r"}},
			"user": {"login": "bot"}
		}`))
	})
	pr, err := client.GetPullRequest(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if pr.Number != 7 || pr.Title != "T" || pr.HeadSHA != "abc" || pr.HeadRef != "feat" ||
		pr.BaseRef != "main" || pr.RepoFullName != "o/r" || pr.HeadRepoFullName != "o/r" || pr.AuthorLogin != "bot" {
		t.Fatalf("映射不符: %+v", pr)
	}
	if pr.ClosedAt == nil || pr.MergedAt != nil {
		t.Fatalf("时间映射不符: %+v", pr)
	}
	got := lastServeLog(t, logs)
	if got.method != http.MethodGet || got.path != "/api/v1/repos/o/r/pulls/7" {
		t.Fatalf("请求形状: %s %s", got.method, got.path)
	}
	if got.auth != "token pat-123" {
		t.Fatalf("认证头 = %q, want token pat-123", got.auth)
	}
}

func TestForgejoClient_GetPullRequestDiff(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("diff --git a/f b/f\n"))
	})
	diff, err := client.GetPullRequestDiff(context.Background(), "o/r", 3)
	if err != nil || !strings.HasPrefix(diff, "diff --git a/f b/f") {
		t.Fatalf("diff = %q, %v", diff, err)
	}
	got := lastServeLog(t, logs)
	if got.path != "/api/v1/repos/o/r/pulls/3.diff" || got.auth != "token pat-123" {
		t.Fatalf("请求形状: %s %s %q", got.method, got.path, got.auth)
	}
}

func TestForgejoClient_CommentsRoundTrip(t *testing.T) {
	// 顶层评论：issue comments 端点 + PATCH /issues/comments/{id}。
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user":
			// 仓库级 PAT：/user 403 → marker-only 退化（本用例不关心自评标记）。
			w.WriteHeader(http.StatusForbidden)
		case r.Method == http.MethodGet && r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1":
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"id": 11, "body": "hi", "user": {"login": "u"}, "created_at": "2026-01-02T03:04:05Z"}]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`{"id": 22}`))
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"id": 22}`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	comments, err := client.ListIssueCommentsForPR(ctx, "o/r", 5)
	if err != nil || len(comments) != 1 || comments[0].ID != 11 || comments[0].User != "u" {
		t.Fatalf("ListIssueCommentsForPR = %+v, %v", comments, err)
	}
	id, err := client.CreatePullRequestComment(ctx, "o/r", 5, "body")
	if err != nil || id != 22 {
		t.Fatalf("CreatePullRequestComment = %d, %v", id, err)
	}
	log := lastServeLog(t, logs)
	if log.path != "/api/v1/repos/o/r/issues/5/comments" {
		t.Fatalf("create path = %s", log.path)
	}
	if got := jsonBody(t, log.body); got["body"] != "body" {
		t.Fatalf("create body = %v", got)
	}
	if err := client.UpdatePullRequestComment(ctx, "o/r", 22, "v2"); err != nil {
		t.Fatalf("UpdatePullRequestComment: %v", err)
	}
	log = lastServeLog(t, logs)
	if log.method != http.MethodPatch || log.path != "/api/v1/repos/o/r/issues/comments/22" {
		t.Fatalf("update 形状: %s %s", log.method, log.path)
	}
}

// TestForgejoClient_FindPullRequestComment 认自己的评论：先 GET /user 学到
// PAT 所属账号，再按「作者 == 自己 + marker 前缀」命中；用户评论引用 marker
// 不算。
func TestForgejoClient_FindPullRequestComment(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/user":
			_, _ = w.Write([]byte(`{"login": "fast-ship-bot"}`))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[
				{"id": 1, "body": "<!-- fast-ship-code-review --> 冒充", "user": {"login": "mallory"}},
				{"id": 2, "body": "<!-- fast-ship-code-review --> 真身", "user": {"login": "fast-ship-bot"}}
			]`))
		default:
			http.NotFound(w, r)
		}
	})
	id, found, err := client.FindPullRequestComment(context.Background(), "o/r", 5, "<!-- fast-ship-code-review -->")
	if err != nil || !found || id != 2 {
		t.Fatalf("FindPullRequestComment = (%d,%v,%v), want (2,true,nil)", id, found, err)
	}
	first := (*logs)[0]
	if first.path != "/api/v1/user" {
		t.Fatalf("第一请求应先学账号, got %s", first.path)
	}
}

// TestForgejoClient_FindPullRequestComment_NoUserScope 仓库范围 PAT 下
// GET /user 403（Forgejo 不给 specific-repo token 授 user/* scope）：对账
// 不得挂死——退化 marker-only 命中首条 marker 评论；一次写响应学到 login
// 后恢复「作者==自己」过滤。
func TestForgejoClient_FindPullRequestComment_NoUserScope(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/user":
			w.WriteHeader(http.StatusForbidden)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			// CreatePullRequestComment 响应：Forgejo 返回完整评论对象。
			_, _ = w.Write([]byte(`{"id": 30, "body": "x", "user": {"login": "fast-ship-bot"}}`))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[
				{"id": 1, "body": "<!-- fast-ship-code-review --> 冒充", "user": {"login": "mallory"}},
				{"id": 2, "body": "<!-- fast-ship-code-review --> 真身", "user": {"login": "fast-ship-bot"}}
			]`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	// /user 403：退化 marker-only，命中列表首条 marker（残余的伪造覆写
	// 窗口，文档已注明）——但不能返回错误让评论永远发不出。
	id, found, err := client.FindPullRequestComment(ctx, "o/r", 5, "<!-- fast-ship-code-review -->")
	if err != nil || !found || id != 1 {
		t.Fatalf("no-scope FindPullRequestComment = (%d,%v,%v), want (1,true,nil)", id, found, err)
	}

	// 一次写响应学到 login 后，同一调用恢复按作者过滤（伪造评论被跳过）。
	if _, err := client.CreatePullRequestComment(ctx, "o/r", 5, "b"); err != nil {
		t.Fatalf("CreatePullRequestComment: %v", err)
	}
	id, found, err = client.FindPullRequestComment(ctx, "o/r", 5, "<!-- fast-ship-code-review -->")
	if err != nil || !found || id != 2 {
		t.Fatalf("learned-login FindPullRequestComment = (%d,%v,%v), want (2,true,nil)", id, found, err)
	}
}

// TestForgejoClient_SelfLogin_TransientErrorPropagates /user 抖动不降级：
// 5xx/网络错误照旧传播挂起本轮——瞬时故障降级 marker-only 会把伪造覆写
// 窗口扩大到任意故障时刻（fsb 第 4 轮 P2）。非限流 403 才是退化信号。
func TestForgejoClient_SelfLogin_TransientErrorPropagates(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	_, _, err := client.FindPullRequestComment(context.Background(), "o/r", 5, "<!-- m -->")
	if err == nil {
		t.Fatal("/user 5xx 应传播错误，不得退化 marker-only")
	}
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusBadGateway {
		t.Fatalf("err = %v, want *StatusError 502", err)
	}
}

// TestForgejoClient_SelfLogin_NotFoundPropagates /user 404 不降级：仓库级
// PAT 缺 read:user 的确定性信号是 403 不是 404——404 更像 api_base_url
// 指错路径前缀，静默降级会把「作者==自己」过滤关掉且不留报错线索。
func TestForgejoClient_SelfLogin_NotFoundPropagates(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	_, _, err := client.FindPullRequestComment(context.Background(), "o/r", 5, "<!-- m -->")
	if err == nil {
		t.Fatal("/user 404 应传播错误，不得退化 marker-only")
	}
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want *StatusError 404", err)
	}
}

// TestForgejoClient_SelfLogin_RateLimited403Propagates 限流 403 与权限 403
// 无法从状态码区分（GitHub 主/二次限流正是回 403）——自评探测把限流窗口的
// /user 403 当「仓库级 PAT 无 read:user」确定性信号静默降级的话，整窗关
// 掉「作者==自己」过滤：伪造 marker 可被覆写、机器人自己的评论 Self=false
// 回流历史。先判 IsRateLimit，限流 403 照旧传播挂起本轮。
func TestForgejoClient_SelfLogin_RateLimited403Propagates(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	_, _, err := client.FindPullRequestComment(context.Background(), "o/r", 5, "<!-- m -->")
	if err == nil {
		t.Fatal("限流 403 应传播错误挂起本轮，不得退化 marker-only")
	}
	if !IsRateLimit(err) {
		t.Fatalf("err = %v, want IsRateLimit", err)
	}
}

// TestForgejoClient_StatusErr_RateLimitInTruncatedMiddle 大 HTML 错误页的
// 限流字样恰好落在被截断丢弃的中段时，IsRateLimit 仍按构造处对完整 body
// 的预判定为真——否则限流 403 被误判成权限 403（确定性失败 → 误删防抖行/
// 误停用审查，与自评探测防的是同一类误判）。
func TestForgejoClient_StatusErr_RateLimitInTruncatedMiddle(t *testing.T) {
	big := strings.Repeat("x", 1000) + " rate limit exceeded " + strings.Repeat("y", 1000)
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(big))
	})
	_, err := client.GetPullRequest(context.Background(), "o/r", 5)
	if err == nil {
		t.Fatal("403 应返回错误")
	}
	var se *StatusError
	if !errors.As(err, &se) || len(se.Body) >= len(big) {
		t.Fatalf("Body 应已被截断: %v", err)
	}
	if !IsRateLimit(err) {
		t.Fatalf("中段限流字样被截断后仍应判 IsRateLimit: %v", err)
	}
}

// TestForgejoClient_UpdateComment_NoLearnForeignLogin PATCH 响应的 user 是
// 原作者不是编辑者：marker-only 命中他人伪造评论时不得把其 login 学进缓存
// （否则 Self/对账认攻击者为自己且持久中毒——fsb 第 5 轮 P2）。
func TestForgejoClient_UpdateComment_NoLearnForeignLogin(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user":
			w.WriteHeader(http.StatusForbidden)
		case r.Method == http.MethodPatch:
			// Forgejo PATCH 回评论对象：user=原作者（伪造者），不是 PAT 账号。
			_, _ = w.Write([]byte(`{"id": 1, "body": "x", "user": {"login": "mallory"}}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	if err := client.UpdatePullRequestComment(context.Background(), "o/r", 1, "b"); err != nil {
		t.Fatalf("UpdatePullRequestComment: %v", err)
	}
	if got := client.loginCache.login; got != "" {
		t.Fatalf("PATCH 响应不得学习 login，got %q", got)
	}
}

// TestForgejoClient_SharedLoginCache 跨实例身份：写响应学到的 login 存进
// resolver 分槽共享缓存（/user 恒 403 的仓库级 PAT 下 Self 标记才不致随
// 实例重建永久失效）——第二个实例不再探测 /user 也能标 Self。
func TestForgejoClient_SharedLoginCache(t *testing.T) {
	var userCalls int
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/user":
			userCalls++
			w.WriteHeader(http.StatusForbidden)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`{"id": 30, "body": "x", "user": {"login": "fast-ship-bot"}}`))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[
				{"id": 1, "body": "b1", "user": {"login": "fast-ship-bot"}},
				{"id": 2, "body": "b2", "user": {"login": "mallory"}}
			]`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	shared := &ForgejoLoginCache{}

	// 实例 A：一次写学到 login（/user 403 → 写响应 user.login）。
	a := client.withLoginCache(shared)
	if _, err := a.CreatePullRequestComment(ctx, "o/r", 5, "b"); err != nil {
		t.Fatalf("CreatePullRequestComment: %v", err)
	}

	// 实例 B：换共享槽的新适配器——不再发 /user 探测，Self 直接生效。
	b := NewForgejoReviewClient(client.h, client.apiBase, client.token).withLoginCache(shared)
	before := userCalls
	got, err := b.ListIssueCommentsForPR(ctx, "o/r", 5)
	if err != nil {
		t.Fatalf("ListIssueCommentsForPR: %v", err)
	}
	if userCalls != before {
		t.Fatalf("共享缓存命中后不应再探测 /user，calls=%d→%d", before, userCalls)
	}
	if len(got) != 2 || !got[0].Self || got[1].Self {
		t.Fatalf("Self 标记 = %+v，want [true,false]", got)
	}
}

func TestForgejoClient_ListReviewsAndComments(t *testing.T) {
	// ListReviews 映射 id/commit_id/state/submitted_at；ListReviewComments 经
	// review 列表逐个取 comments 展平。PENDING 草稿（id 10）对读者不可见：
	// 两边都排除——ListReviews 不回、ListReviewComments 不展开其评论端点。
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/user":
			_, _ = w.Write([]byte(`{"login": "bot"}`))
		case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[
				{"id": 9, "body": "r", "state": "COMMENT", "commit_id": "abc", "submitted_at": "2026-01-02T03:04:05Z", "user": {"login": "bot"}},
				{"id": 10, "body": "draft", "state": "PENDING", "commit_id": "abc", "user": {"login": "bot"}}
			]`))
		case strings.Contains(r.URL.Path, "/reviews/10/comments"):
			t.Error("PENDING review 的评论端点不得被请求")
		case strings.Contains(r.URL.Path, "/reviews/9/comments"):
			_, _ = w.Write([]byte(`[{"id": 31, "body": "n", "path": "a.go", "user": {"login": "bot"}, "created_at": "2026-01-02T03:04:06Z"}]`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	reviews, err := client.ListReviews(ctx, "o/r", 4)
	if err != nil || len(reviews) != 1 {
		t.Fatalf("ListReviews = %+v, %v", reviews, err)
	}
	if reviews[0].ID != 9 || reviews[0].CommitID != "abc" || reviews[0].State != "COMMENT" || reviews[0].CreatedAt.IsZero() {
		t.Fatalf("review 映射不符: %+v", reviews[0])
	}
	comments, err := client.ListReviewComments(ctx, "o/r", 4)
	if err != nil || len(comments) != 1 {
		t.Fatalf("ListReviewComments = %+v, %v", comments, err)
	}
	if comments[0].ID != 31 || comments[0].Path != "a.go" || comments[0].InReplyToID != nil {
		t.Fatalf("行内评论映射不符: %+v", comments[0])
	}
	if !comments[0].Self {
		t.Fatalf("评论作者与 PAT login 相同应标 Self: %+v", comments[0])
	}
	// 请求序列：ListReviews 数据页 + 空页探测；ListReviewComments 内 review
	// 列表重查两页 → /user 探测自评身份 → review 9 的 comments 数据页 +
	// 空页探测。
	if len(*logs) != 7 {
		t.Fatalf("请求数 = %d, want 7", len(*logs))
	}
	paths := []string{
		(*logs)[0].path, (*logs)[1].path, (*logs)[2].path, (*logs)[3].path,
		(*logs)[4].path, (*logs)[5].path, (*logs)[6].path,
	}
	if paths[0] != "/api/v1/repos/o/r/pulls/4/reviews?limit=50&page=1" ||
		paths[1] != "/api/v1/repos/o/r/pulls/4/reviews?limit=50&page=2" ||
		paths[2] != "/api/v1/repos/o/r/pulls/4/reviews?limit=50&page=1" ||
		paths[3] != "/api/v1/repos/o/r/pulls/4/reviews?limit=50&page=2" ||
		paths[4] != "/api/v1/user" ||
		paths[5] != "/api/v1/repos/o/r/pulls/4/reviews/9/comments?limit=50&page=1" ||
		paths[6] != "/api/v1/repos/o/r/pulls/4/reviews/9/comments?limit=50&page=2" {
		t.Fatalf("请求顺序: %v", paths)
	}
}

// TestForgejoClient_ListReviewComments_KeepsNewest 展开数超上限时保留最新段：
// 列表按 id 升序返回，从头数满会恰好把最新 review（用户最新行内讨论所在）
// 截掉。喂 forgejoReviewExpandMax+1 条已提交 review，断言最旧一条的评论
// 端点不被请求、最新一条照常展开、总展开数恰为上限。
func TestForgejoClient_ListReviewComments_KeepsNewest(t *testing.T) {
	var reviewsJSON strings.Builder
	reviewsJSON.WriteString("[")
	for i := 1; i <= forgejoReviewExpandMax+1; i++ {
		if i > 1 {
			reviewsJSON.WriteString(",")
		}
		fmt.Fprintf(&reviewsJSON,
			`{"id":%d,"body":"r","state":"COMMENT","commit_id":"c","user":{"login":"bot"}}`, i)
	}
	reviewsJSON.WriteString("]")
	body := reviewsJSON.String()

	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/user":
			_, _ = w.Write([]byte(`{"login": "bot"}`))
		case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(body))
		case strings.Contains(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	})
	if _, err := client.ListReviewComments(context.Background(), "o/r", 4); err != nil {
		t.Fatalf("ListReviewComments: %v", err)
	}
	expanded := map[int]bool{}
	for _, l := range *logs {
		var id int
		if n, _ := fmt.Sscanf(l.path,
			"/api/v1/repos/o/r/pulls/4/reviews/%d/comments", &id); n == 1 {
			expanded[id] = true
		}
	}
	if expanded[1] {
		t.Fatal("最旧 review 应被截断，其 comments 端点不应请求")
	}
	if !expanded[forgejoReviewExpandMax+1] {
		t.Fatal("最新 review 的 comments 端点必须展开")
	}
	if len(expanded) != forgejoReviewExpandMax {
		t.Fatalf("展开 review 数 = %d, want %d", len(expanded), forgejoReviewExpandMax)
	}
}

// TestForgejoClient_SubmitReview 行内评论映射：RIGHT→new_position（缺省侧同）、
// LEFT→old_position；commit_id 与 event/body 原样带上。
func TestForgejoClient_SubmitReview(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 77}`))
	})
	id, err := client.SubmitReview(context.Background(), "o/r", 4, ReviewInput{
		Event: "COMMENT", Body: "结论", CommitID: "abc",
		Comments: []ReviewInlineComment{
			{Path: "a.go", Line: 12, Side: "RIGHT", Body: "n1"},
			{Path: "b.go", Line: 3, Side: "LEFT", Body: "n2"},
		},
	})
	if err != nil || id != 77 {
		t.Fatalf("SubmitReview = %d, %v", id, err)
	}
	log := lastServeLog(t, logs)
	if log.path != "/api/v1/repos/o/r/pulls/4/reviews" || log.method != http.MethodPost {
		t.Fatalf("形状: %s %s", log.method, log.path)
	}
	got := jsonBody(t, log.body)
	if got["event"] != "COMMENT" || got["commit_id"] != "abc" {
		t.Fatalf("顶层字段: %v", got)
	}
	lines, ok := got["comments"].([]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("comments = %v", got["comments"])
	}
	first := lines[0].(map[string]any)
	second := lines[1].(map[string]any)
	if first["new_position"] != float64(12) || first["path"] != "a.go" {
		t.Fatalf("RIGHT 行映射: %v", first)
	}
	if second["old_position"] != float64(3) || second["path"] != "b.go" {
		t.Fatalf("LEFT 行映射: %v", second)
	}
}

// TestForgejoClient_CommitStatusTruncatesDescription description 按 rune 截断
// 到 255（Gitea CreateStatusOption MaxSize(255)，超出整单 422）。
func TestForgejoClient_CommitStatusTruncatesDescription(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	long := strings.Repeat("好", 300) + "tail"
	if err := client.CreateCommitStatus(context.Background(), "o/r", "abc", "success",
		"代码审查", long, "https://fs.example.com"); err != nil {
		t.Fatalf("CreateCommitStatus: %v", err)
	}
	got := jsonBody(t, lastServeLog(t, logs).body)
	desc, _ := got["description"].(string)
	if runeCount := len([]rune(desc)); runeCount != 255 {
		t.Fatalf("description rune 数 = %d, want 255", runeCount)
	}
	if !strings.HasSuffix(desc, "好") {
		t.Fatal("应按 rune 截断（不留半字符）")
	}
	if got["state"] != "success" || got["context"] != "代码审查" || got["target_url"] == "" {
		t.Fatalf("其余字段: %v", got)
	}
}

func TestForgejoClient_GetRepository(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"full_name": "o/r"}`))
	})
	if err := client.GetRepository(context.Background(), "o/r"); err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if got := lastServeLog(t, logs); got.path != "/api/v1/repos/o/r" {
		t.Fatalf("path = %s", got.path)
	}
}

// TestForgejoClient_ListOpenPullRequests open PR 枚举：跨仓库 fork head
// （head≠base）与已删 fork（head 缺失）的 PR 跳过；仓库本身是 fork 但
// head/base 同仓库的 PR 保留——过滤按 head/base 归属而非 repo.fork。
func TestForgejoClient_ListOpenPullRequests(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "open" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[
			{"number": 1, "title": "same", "head": {"sha": "s1", "ref": "f1", "repo": {"full_name": "o/r", "fork": false}}, "base": {"ref": "main", "repo": {"full_name": "o/r"}}},
			{"number": 2, "title": "fork", "head": {"sha": "s2", "ref": "f2", "repo": {"full_name": "o/other", "fork": true}}, "base": {"ref": "main", "repo": {"full_name": "o/r"}}},
			{"number": 3, "title": "deleted-fork", "head": {"sha": "s3", "ref": "f3"}},
			{"number": 4, "title": "case", "head": {"sha": "s4", "ref": "f4", "repo": {"full_name": "O/R", "fork": false}}, "base": {"ref": "main", "repo": {"full_name": "o/r"}}},
			{"number": 5, "title": "forked-repo-same-head", "head": {"sha": "s5", "ref": "f5", "repo": {"full_name": "o/r", "fork": true}}, "base": {"ref": "main", "repo": {"full_name": "o/r", "fork": true}}}
		]`))
	})
	prs, err := client.ListOpenPullRequests(context.Background(), "o/r")
	if err != nil {
		t.Fatalf("ListOpenPullRequests: %v", err)
	}
	// PR5 的仓库本身是 fork（head.repo.fork=true），但 head/base 同仓库——
	// 过滤按 head/base 归属判定，不看 repo.fork，必须保留。
	if len(prs) != 3 || prs[0].Number != 1 || prs[1].Number != 4 || prs[2].Number != 5 {
		t.Fatalf("应只剩同仓库分支 1/4/5: %+v", prs)
	}
	if prs[0].RepoFullName != "o/r" || prs[0].HeadSHA != "s1" {
		t.Fatalf("映射不符: %+v", prs[0])
	}
	got := (*logs)[0]
	if got.path != "/api/v1/repos/o/r/pulls?state=open&limit=50&page=1" {
		t.Fatalf("请求形状: %s", got.path)
	}
}

// TestForgejoClient_StatusErrors 404 → IsNotFound 命中（forge.StatusError 形态）；
// 403 → IsForbidden；错误文案含方法与状态码。
func TestForgejoClient_StatusErrors(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/o/r/pulls/404":
			http.NotFound(w, r)
		case "/api/v1/repos/o/r":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "no access"}`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	_, err := client.GetPullRequest(ctx, "o/r", 404)
	if !IsNotFound(err) {
		t.Fatalf("404 应命中 IsNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("错误文案缺状态码: %v", err)
	}
	if err := client.GetRepository(ctx, "o/r"); !IsForbidden(err) {
		t.Fatalf("403 应命中 IsForbidden, got %v", err)
	}
}

// TestForgejoClient_Pagination 分页到空页止：服务端按部署 MaxResponseItems
// 收缩实际页大小（每页 20 条、不足 limit=50 也要继续翻），结果按序拼接；
// 空页才停。
func TestForgejoClient_Pagination(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1", "2":
			items := strings.TrimSpace(strings.Repeat(`{"id": 1, "body": "b", "user": {"login": "u"}},`, 20))
			items = strings.TrimSuffix(items, ",")
			_, _ = w.Write([]byte("[" + items + "]"))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	comments, err := client.ListIssueCommentsForPR(context.Background(), "o/r", 5)
	if err != nil || len(comments) != 40 {
		t.Fatalf("comments = %d, %v, want 40（缩页不得截断后续页）", len(comments), err)
	}
	// 两页数据 + 一页空页 + 一次 /user 自评身份探测。
	if n := len(*logs); n != 4 {
		t.Fatalf("请求数 = %d, want 4", n)
	}
}

func TestForgejoClient_KindAndBearer(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	if client.Kind() != KindForgejo {
		t.Fatalf("Kind = %q", client.Kind())
	}
	tok, err := client.Bearer(context.Background())
	if err != nil || tok != "pat-123" {
		t.Fatalf("Bearer = (%q,%v), want (pat-123,nil)", tok, err)
	}
}

// TestForgejoClient_InvalidRepoName 非法仓库名在发出请求前报错。
func TestForgejoClient_InvalidRepoName(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, repo := range []string{"", "o", "o/r/e"} {
		if _, err := client.GetPullRequest(context.Background(), repo, 1); err == nil {
			t.Fatalf("repo=%q 应报错", repo)
		}
	}
	if len(*logs) != 0 {
		t.Fatal("非法仓库名不应发出请求")
	}
}

// TestForgejoClient_GetPullRequestDiff_TooLarge 本端截断探测超上限返回
// ErrDiffTooLarge 哨兵，IsDiffTooLarge 与平台 too_large 同口径命中——
// service 侧进降级收尾，不再按可重试错误无限退避；返回值携带已读前缀
// （AgentDiff 端点按截断件给 Agent，不每次 50200 卡到 deadline）。
func TestForgejoClient_GetPullRequestDiff_TooLarge(t *testing.T) {
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(make([]byte, respBodyMaxBytes+1))
	})
	diff, err := client.GetPullRequestDiff(context.Background(), "o/r", 3)
	if !IsDiffTooLarge(err) {
		t.Fatalf("超限 diff 应命中 IsDiffTooLarge, got %v", err)
	}
	if !errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("应保留 ErrDiffTooLarge 哨兵, got %v", err)
	}
	if len(diff) != respBodyMaxBytes {
		t.Fatalf("前缀 len = %d, want %d", len(diff), respBodyMaxBytes)
	}
}

// TestForgejoClient_ListPullRequestFiles_MaxResponseItemsShrink 部署侧
// MaxResponseItems 收缩实际页大小时（limit=100 只回 20）继续翻页到空页——
// 「不满一页即末页」会把后续页的删除/重命名路径静默漏掉（r11 P1）。
func TestForgejoClient_ListPullRequestFiles_MaxResponseItemsShrink(t *testing.T) {
	const perPage = 20 // 模拟 MaxResponseItems=20 的收缩部署
	client, _ := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		start := (page - 1) * perPage
		items := "["
		for i := start; i < start+perPage && i < 45; i++ {
			if i > start {
				items += ","
			}
			items += fmt.Sprintf(`{"filename":"f%d.go","status":"modified"}`, i)
		}
		_, _ = w.Write([]byte(items + "]"))
	})
	paths, complete, err := client.ListPullRequestFiles(context.Background(), "o/r", 3)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if !complete || len(paths) != 45 {
		t.Fatalf("complete=%v paths=%d, want complete 45", complete, len(paths))
	}
}

// TestForgejoClient_PathExistsAtRef INT-169 文件归属的 Forgejo 实现：contents
// 端点 2xx→存在、404 经根目录探测消歧（根在→不存在，根也 404→调用失败）、
// 非规范路径本地判不存在且零请求。
func TestForgejoClient_PathExistsAtRef(t *testing.T) {
	client, logs := newForgejoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") == "bad-ref" {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "gone.go") {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"a.go","path":"dir/a.go","type":"file"}`))
	})
	ctx := context.Background()

	ok, err := client.PathExistsAtRef(ctx, "o/r", "dir/a.go", "sha-1")
	if err != nil || !ok {
		t.Fatalf("存在路径 = %v, %v", ok, err)
	}
	got := lastServeLog(t, logs)
	if got.method != http.MethodGet ||
		got.path != "/api/v1/repos/o/r/contents/dir/a.go?ref=sha-1" {
		t.Fatalf("请求形状: %s %s", got.method, got.path)
	}

	// 路径 404 + 根目录 200 → 路径确实不存在。
	ok, err = client.PathExistsAtRef(ctx, "o/r", "gone.go", "sha-1")
	if err != nil || ok {
		t.Fatalf("404 = %v, %v, want false,nil", ok, err)
	}
	got = lastServeLog(t, logs)
	if got.path != "/api/v1/repos/o/r/contents/?ref=sha-1" {
		t.Fatalf("消歧应探测根目录, got %q", got.path)
	}

	// 根目录也 404 → ref/仓库级故障，报错而非「不存在」。
	if _, err = client.PathExistsAtRef(ctx, "o/r", "any.go", "bad-ref"); err == nil {
		t.Fatal("ref 级 404 须透传为错误")
	}

	for _, bad := range []string{"/a.go", "a//b.go", "./a.go", "../x", "a.go/"} {
		before := len(*logs)
		ok, err = client.PathExistsAtRef(ctx, "o/r", bad, "sha-1")
		if err != nil || ok || len(*logs) != before {
			t.Fatalf("别名 %q = %v, %v, 请求数 %d→%d", bad, ok, err, before, len(*logs))
		}
	}
}
