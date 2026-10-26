package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAuth 固定 bearer 并记录 Invalidate 次数。
type fakeAuth struct {
	token      string
	bearerHits int
	invalids   int
	failBearer bool
}

func (a *fakeAuth) Bearer(ctx context.Context, installationID int64) (string, error) {
	if a.failBearer {
		return "", errors.New("bearer unavailable")
	}
	a.bearerHits++
	return a.token, nil
}

func (a *fakeAuth) Invalidate(installationID int64) {
	a.invalids++
}

func TestGetPullRequest_MapsAllFields(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octocat/hw/pulls/7" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization = %q, want bearer token", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"number":    7,
			"title":     "Fix crash",
			"body":      "long story",
			"html_url":  "https://github.com/octocat/hw/pull/7",
			"state":     "closed",
			"draft":     true,
			"merged":    true,
			"merged_at": "2026-01-02T03:04:05Z",
			"closed_at": "2026-01-02T03:04:06Z",
			"user":      map[string]any{"login": "bob"},
			"head":      map[string]any{"sha": "abc123", "ref": "fix-crash", "repo": map[string]any{"full_name": "fork/other"}},
			"base":      map[string]any{"ref": "main", "repo": map[string]any{"full_name": "octocat/hw"}},
		})
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "tok"}
	c := NewClient(auth, ts.URL)
	pr, err := c.GetPullRequest(context.Background(), 1, "octocat/hw", 7)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	mergedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	closedAt := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	want := PullRequest{
		RepoFullName: "octocat/hw",
		Number:       7,
		Title:        "Fix crash",
		Body:         "long story",
		HTMLURL:      "https://github.com/octocat/hw/pull/7",
		State:        "closed",
		Draft:        true,
		Merged:       true,
		MergedAt:     &mergedAt,
		ClosedAt:     &closedAt,
		HeadSHA:      "abc123",
		HeadRef:      "fix-crash",
		BaseRef:      "main",
		AuthorLogin:  "bob",
	}
	if !reflect.DeepEqual(*pr, want) {
		t.Fatalf("pull request mismatch:\n got %+v\nwant %+v", *pr, want)
	}
}

func TestRESTClient_UnauthorizedInvalidatesAndRetriesOnce(t *testing.T) {
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 9001, "body": "ok"})
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "stale"}
	c := NewClient(auth, ts.URL)
	commentID, err := c.CreatePullRequestComment(context.Background(), 1, "octocat/hw", 7, "hi")
	if err != nil {
		t.Fatalf("CreatePullRequestComment: %v", err)
	}
	if commentID != 9001 {
		t.Fatalf("comment id = %d, want 9001", commentID)
	}
	if auth.invalids != 1 || auth.bearerHits != 2 {
		t.Fatalf("invalidate=%d bearer=%d, want 1/2", auth.invalids, auth.bearerHits)
	}
}

func TestRESTClient_PersistentUnauthorizedPropagatesStatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "stale"}
	c := NewClient(auth, ts.URL)
	_, err := c.GetPullRequestDiff(context.Background(), 1, "octocat/hw", 7)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected *StatusError 401, got %v", err)
	}
	if auth.invalids != 1 || auth.bearerHits != 2 {
		t.Fatalf("invalidate=%d bearer=%d, want 1/2", auth.invalids, auth.bearerHits)
	}
}

// 2xx 响应体超上限确定性报 ErrBodyTooLarge 并携带已读前缀（前缀仍是真实
// 内容，AgentDiff 端点按截断件用）——静默截断的 diff 会被当成完整变更清单，
// 不能放行；非 2xx 大响应仍截断走 StatusError（错误路径语义不变）。
func TestBodyTooLargeDetection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls/7") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(make([]byte, diffBodyMaxBytes+1))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(make([]byte, bodyReadMaxBytes+1))
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "t"}
	c := NewClient(auth, ts.URL)
	diff, err := c.GetPullRequestDiff(context.Background(), 1, "octocat/hw", 7)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("超限 diff 应报 ErrBodyTooLarge, got %v", err)
	}
	if len(diff) != diffBodyMaxBytes {
		t.Fatalf("前缀 len = %d, want %d", len(diff), diffBodyMaxBytes)
	}

	err = c.GetRepository(context.Background(), 1, "octocat/hw")
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusInternalServerError {
		t.Fatalf("非 2xx 大响应应仍走 StatusError, got %v", err)
	}
	if errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("错误响应不应报 ErrBodyTooLarge, got %v", err)
	}
}

// 目录清单超读入上限（>8MiB）按 ErrTreeTruncated 不可判定处理——不完整
// 证据不能当缺席证明，也不能让归属校验每次 50200 卡到 deadline。
func TestPathExistsAtRefOversizedTree(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, bodyReadMaxBytes+1))
	}))
	defer ts.Close()

	c := NewClient(&fakeAuth{token: "t"}, ts.URL)
	_, err := c.PathExistsAtRef(context.Background(), 1, "octocat/hw", "bigdir/f.go", "sha-x")
	if !errors.Is(err, ErrTreeTruncated) {
		t.Fatalf("oversized tree = %v, want ErrTreeTruncated", err)
	}
}

// ListPullRequestFiles 分页收口 + rename 两侧收录 + 3000 封顶判不完整。
func TestListPullRequestFiles(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			// 满页：rename 的 previous_filename 也要收。
			items := `[{"filename":"new.go","previous_filename":"old.go","status":"renamed"}`
			for i := 0; i < 99; i++ {
				items += fmt.Sprintf(`,{"filename":"f%d.go","status":"modified"}`, i)
			}
			_, _ = w.Write([]byte(items + "]"))
		default:
			_, _ = w.Write([]byte(`[{"filename":"tail.go","status":"removed"}]`))
		}
	}))
	defer ts.Close()

	c := NewClient(&fakeAuth{token: "t"}, ts.URL)
	paths, complete, err := c.ListPullRequestFiles(context.Background(), 1, "octocat/hw", 9)
	if err != nil || !complete {
		t.Fatalf("paths=%v complete=%v err=%v", paths, complete, err)
	}
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	for _, want := range []string{"new.go", "old.go", "f0.go", "tail.go"} {
		if !set[want] {
			t.Fatalf("missing %q in %v", want, paths)
		}
	}
}

func TestStatusErrorHelpers(t *testing.T) {
	if !IsForbidden(&StatusError{StatusCode: http.StatusForbidden}) {
		t.Fatal("403 must be forbidden")
	}
	if !IsForbidden(ErrForbidden) {
		t.Fatal("sentinel must be forbidden")
	}
	if !IsNotFound(&StatusError{StatusCode: http.StatusNotFound}) {
		t.Fatal("404 must be not found")
	}
	if IsForbidden(&StatusError{StatusCode: http.StatusBadRequest}) {
		t.Fatal("400 must not be forbidden")
	}
}

func TestSplitRepo(t *testing.T) {
	owner, name, err := splitRepo("octocat/hw")
	if err != nil || owner != "octocat" || name != "hw" {
		t.Fatalf("splitRepo = %q %q %v", owner, name, err)
	}
	if _, _, err := splitRepo("octocat"); err == nil {
		t.Fatal("single segment must fail")
	}
	if _, _, err := splitRepo("a/b/c"); err == nil {
		t.Fatal("three segments must fail")
	}
}

// TestFindPullRequestComment_PaginatesToPage2 marker 在第 2 页才出现：
// 首页满页时继续翻页，命中即停。
func TestFindPullRequestComment_PaginatesToPage2(t *testing.T) {
	var pages []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octocat/hw/issues/7/comments" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		pages = append(pages, r.URL.Query().Get("page"))
		switch r.URL.Query().Get("page") {
		case "1":
			wire := make([]map[string]any, 0, reviewCommentPageSize)
			for i := 0; i < reviewCommentPageSize; i++ {
				wire = append(wire, map[string]any{
					"id": 1000 + i, "body": "regular comment",
					"user": map[string]any{"login": "someone", "type": "User"},
				})
			}
			writeJSON(t, w, http.StatusOK, wire)
		default:
			writeJSON(t, w, http.StatusOK, []map[string]any{
				{"id": 42, "body": "<!-- fast-ship-code-review --> conclusion",
					"user": map[string]any{"login": "fast-ship[bot]", "type": "Bot"}},
			})
		}
	}))
	defer ts.Close()

	auth := &fakeAuth{token: "tok"}
	c := NewClient(auth, ts.URL)
	id, found, err := c.FindPullRequestComment(context.Background(), 1, "octocat/hw", 7, "<!-- fast-ship-code-review -->")
	if err != nil {
		t.Fatalf("FindPullRequestComment: %v", err)
	}
	if !found || id != 42 {
		t.Fatalf("id/found = %d/%v, want 42/true", id, found)
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("pages = %v, want [1 2]", pages)
	}
}

// TestPathExistsAtRef 非递归 git/trees 归属判定（INT-169）：URL 形状
// /git/trees/{ref}:{dir}、父目录条目精确匹配末段名、404→不存在、
// 截断未命中→报错、非规范路径本地拒绝且不发请求。
func TestPathExistsAtRef(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		writeJSON(t, w, http.StatusOK, map[string]any{
			"tree": []map[string]any{
				{"path": "a.go", "type": "blob"},
				{"path": "sub", "type": "tree"},
			},
			"truncated": false,
		})
	}))
	defer ts.Close()
	c := NewClient(&fakeAuth{token: "tok"}, ts.URL)
	ctx := context.Background()

	// 命中末段条目 → true；URL 是 /git/trees/{ref}:{dir}（dir 段逐段转义、
	// 以字面 / 拼接）。
	ok, err := c.PathExistsAtRef(ctx, 1, "octocat/hw", "dir/sub/a.go", "sha-1")
	if err != nil || !ok {
		t.Fatalf("exists = %v %v, want true", ok, err)
	}
	if gotPath != "/repos/octocat/hw/git/trees/sha-1:dir/sub" {
		t.Fatalf("request path = %q, want git/trees/sha-1:dir/sub", gotPath)
	}
	// 父目录存在但末段不在条目中 → false（b.go 不在 tree 列表）。
	ok, err = c.PathExistsAtRef(ctx, 1, "octocat/hw", "dir/sub/b.go", "sha-1")
	if err != nil || ok {
		t.Fatalf("missing entry = %v %v, want false nil", ok, err)
	}
	// 根级文件：treeish 无 :dir 后缀。
	ok, err = c.PathExistsAtRef(ctx, 1, "octocat/hw", "a.go", "sha-1")
	if err != nil || !ok {
		t.Fatalf("root file = %v %v, want true", ok, err)
	}
	if gotPath != "/repos/octocat/hw/git/trees/sha-1" {
		t.Fatalf("root request path = %q, want git/trees/sha-1", gotPath)
	}
	// 含空格/中文的路径段必须转义发出。
	ok, err = c.PathExistsAtRef(ctx, 1, "octocat/hw", "docs/设计 文档/a.go", "sha-1")
	if err != nil || !ok {
		t.Fatalf("unicode path = %v %v, want true", ok, err)
	}
	if gotPath != "/repos/octocat/hw/git/trees/sha-1:docs/%E8%AE%BE%E8%AE%A1%20%E6%96%87%E6%A1%A3" {
		t.Fatalf("unicode request path = %q", gotPath)
	}
	// 非规范路径：本地判不存在，请求不得发出。
	for _, bad := range []string{"/a.go", "a//b.go", "./a.go", "a.go/./b.go", "../x", "a.go/../b.go", "a.go/", ""} {
		gotPath = ""
		ok, err = c.PathExistsAtRef(ctx, 1, "octocat/hw", bad, "sha-1")
		if err != nil || ok {
			t.Fatalf("alias %q = %v %v, want false nil", bad, ok, err)
		}
		if gotPath != "" {
			t.Fatalf("alias %q must not issue HTTP request, got %q", bad, gotPath)
		}
	}
}

// TestPathExistsAtRefNotFoundAndTruncated 404 消歧（r2 Note）：dir 级
// 404 会回探根树——根在则路径不存在（false,nil），根也 404/出错则按
// 调用失败上报（ref/仓库不可达不能误报成「文件不存在」）；根级请求
// 自身 404 恒为错误。tree 截断且未命中→ErrTreeTruncated（r4：不可判定，
// 「不在截断清单内」不能当缺席证明，service 按放行+Warn 而非 50200）。
func TestPathExistsAtRefNotFoundAndTruncated(t *testing.T) {
	var rootStatus int
	var rootPayload any
	dirStatus := http.StatusNotFound
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// dir 级请求（treeish 含 :）按 dirStatus 应答；根树按 rootStatus。
		if strings.Contains(r.URL.Path, ":") {
			writeJSON(t, w, dirStatus, map[string]any{"message": "dir response"})
			return
		}
		writeJSON(t, w, rootStatus, rootPayload)
	}))
	defer ts.Close()
	c := NewClient(&fakeAuth{token: "tok"}, ts.URL)
	ctx := context.Background()

	// dir 404 + 根树 200 → 目录确实缺，路径不存在。
	rootStatus, rootPayload = http.StatusOK, map[string]any{
		"tree": []map[string]any{{"path": "other.go", "type": "blob"}},
	}
	ok, err := c.PathExistsAtRef(ctx, 1, "octocat/hw", "x/y.go", "sha-1")
	if err != nil || ok {
		t.Fatalf("dir 404 + root 200 = %v %v, want false nil", ok, err)
	}

	// dir 404 + 根树 404 → ref/仓库级故障，报错而非「不存在」。
	rootStatus, rootPayload = http.StatusNotFound, map[string]any{"message": "Not Found"}
	if _, err = c.PathExistsAtRef(ctx, 1, "octocat/hw", "x/y.go", "sha-1"); err == nil {
		t.Fatal("root probe 404 must surface as error, not file-not-found")
	}

	// 根级路径请求 404（本身就是根树）→ 恒为 ref 级错误。
	if _, err = c.PathExistsAtRef(ctx, 1, "octocat/hw", "y.go", "sha-1"); err == nil {
		t.Fatal("root request 404 must error")
	}

	// 根树 200 但截断且未命中 → ErrTreeTruncated（用根级路径直查根树）。
	rootStatus, rootPayload = http.StatusOK, map[string]any{
		"tree":      []map[string]any{{"path": "other.go", "type": "blob"}},
		"truncated": true,
	}
	if _, err := c.PathExistsAtRef(ctx, 1, "octocat/hw", "y.go", "sha-1"); !errors.Is(err, ErrTreeTruncated) {
		t.Fatalf("truncated miss = %v, want ErrTreeTruncated", err)
	}

	// dir 请求本身 5xx → 透传（不进根探）。
	dirStatus = http.StatusInternalServerError
	if _, err := c.PathExistsAtRef(ctx, 1, "octocat/hw", "x/y.go", "sha-1"); err == nil {
		t.Fatal("5xx must propagate")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode json: %v", err)
	}
}
