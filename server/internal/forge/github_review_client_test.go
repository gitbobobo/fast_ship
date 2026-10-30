package forge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
)

// recordingAPI 记录每次调用的 installationID 与关键入参，返回字段值各异的
// 数据，供适配器透传/转换断言。
type recordingAPI struct {
	inst []int64 // 每次调用收到的 installationID（按调用序）

	pr             *githubapi.PullRequest
	diff           string
	diffErr        error
	reviewComments []githubapi.ReviewComment
	issueComments  []githubapi.IssueComment
	foundID        int64
	found          bool
	createdID      int64
	updatedBody    string
	reviews        []githubapi.ReviewSummary
	reviewID       int64
	reviewInput    githubapi.ReviewInput
	status         statusCall
	repoErr        error
	pathExists     bool
	pathErr        error
}

// statusCall 一次 CreateCommitStatus 的入参快照。
type statusCall struct {
	repo, sha, state, statusContext, description, targetURL string
}

func (r *recordingAPI) mark(installationID int64) { r.inst = append(r.inst, installationID) }

func (r *recordingAPI) last() int64 {
	if len(r.inst) == 0 {
		return -1
	}
	return r.inst[len(r.inst)-1]
}

func (r *recordingAPI) GetPullRequest(_ context.Context, installationID int64, _ string, _ int) (*githubapi.PullRequest, error) {
	r.mark(installationID)
	return r.pr, nil
}

func (r *recordingAPI) GetPullRequestDiff(_ context.Context, installationID int64, _ string, _ int) (string, error) {
	r.mark(installationID)
	return r.diff, r.diffErr
}

func (r *recordingAPI) ListReviewComments(_ context.Context, installationID int64, _ string, _ int) ([]githubapi.ReviewComment, error) {
	r.mark(installationID)
	return r.reviewComments, nil
}

func (r *recordingAPI) ListIssueCommentsForPR(_ context.Context, installationID int64, _ string, _ int) ([]githubapi.IssueComment, error) {
	r.mark(installationID)
	return r.issueComments, nil
}

func (r *recordingAPI) FindPullRequestComment(_ context.Context, installationID int64, _ string, _ int, _ string) (int64, bool, error) {
	r.mark(installationID)
	return r.foundID, r.found, nil
}

func (r *recordingAPI) CreatePullRequestComment(_ context.Context, installationID int64, _ string, _ int, _ string) (int64, error) {
	r.mark(installationID)
	return r.createdID, nil
}

func (r *recordingAPI) UpdatePullRequestComment(_ context.Context, installationID int64, _ string, _ int64, body string) error {
	r.mark(installationID)
	r.updatedBody = body
	return nil
}

func (r *recordingAPI) SubmitReview(_ context.Context, installationID int64, _ string, _ int, in githubapi.ReviewInput) (int64, error) {
	r.mark(installationID)
	r.reviewInput = in
	return r.reviewID, nil
}

func (r *recordingAPI) ListReviews(_ context.Context, installationID int64, _ string, _ int) ([]githubapi.ReviewSummary, error) {
	r.mark(installationID)
	return r.reviews, nil
}

func (r *recordingAPI) CreateCommitStatus(_ context.Context, installationID int64, repo, sha, state, statusContext, description, targetURL string) error {
	r.mark(installationID)
	r.status = statusCall{repo: repo, sha: sha, state: state, statusContext: statusContext, description: description, targetURL: targetURL}
	return nil
}

func (r *recordingAPI) GetRepository(_ context.Context, installationID int64, _ string) error {
	r.mark(installationID)
	return r.repoErr
}

func (r *recordingAPI) ListPullRequestFiles(_ context.Context, installationID int64, _ string, _ int) ([]string, bool, error) {
	r.inst = append(r.inst, installationID)
	return nil, true, nil
}

func (r *recordingAPI) PathExistsAtRef(_ context.Context, installationID int64, _, _, _ string) (bool, error) {
	r.mark(installationID)
	return r.pathExists, r.pathErr
}

func (r *recordingAPI) ResolveInstallation(_ context.Context, _ string) (int64, error) {
	return 0, errors.New("recordingAPI: 不支持解析")
}

// withBearer 模拟 githubapp.Client 的凭证面：Bearer(ctx, installationID)。
type withBearer struct {
	*recordingAPI
	token string
}

func (w *withBearer) Bearer(_ context.Context, installationID int64) (string, error) {
	w.mark(installationID)
	return w.token, nil
}

// ErrBodyTooLarge（diff 端点本端截断探测）归一到 ErrDiffTooLarge，与
// Forgejo 适配器同一哨兵口径。
func TestGitHubReviewClientDiffTooLarge(t *testing.T) {
	api := &recordingAPI{diffErr: githubapi.ErrBodyTooLarge}
	client := NewGitHubReviewClient(api, 1)
	_, err := client.GetPullRequestDiff(context.Background(), "o/r", 1)
	if !errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("应归一到 ErrDiffTooLarge, got %v", err)
	}
	if !IsDiffTooLarge(err) {
		t.Fatalf("IsDiffTooLarge 应命中, got %v", err)
	}
	// 非截断错误原样透传。
	api.diffErr = fmt.Errorf("boom")
	if _, err := client.GetPullRequestDiff(context.Background(), "o/r", 1); errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("普通错误不应归一为 ErrDiffTooLarge, got %v", err)
	}
}

func TestGitHubReviewClientPassesInstallationID(t *testing.T) {
	const inst = int64(42)
	api := &recordingAPI{}
	client := NewGitHubReviewClient(api, inst)
	ctx := context.Background()

	if _, err := client.GetPullRequest(ctx, "o/r", 1); err != nil || api.last() != inst {
		t.Fatalf("GetPullRequest installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if _, err := client.GetPullRequestDiff(ctx, "o/r", 1); err != nil || api.last() != inst {
		t.Fatalf("GetPullRequestDiff installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if _, err := client.ListReviewComments(ctx, "o/r", 1); err != nil || api.last() != inst {
		t.Fatalf("ListReviewComments installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if _, err := client.ListIssueCommentsForPR(ctx, "o/r", 1); err != nil || api.last() != inst {
		t.Fatalf("ListIssueCommentsForPR installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if _, _, err := client.FindPullRequestComment(ctx, "o/r", 1, "m"); err != nil || api.last() != inst {
		t.Fatalf("FindPullRequestComment installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if _, err := client.CreatePullRequestComment(ctx, "o/r", 1, "b"); err != nil || api.last() != inst {
		t.Fatalf("CreatePullRequestComment installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if err := client.UpdatePullRequestComment(ctx, "o/r", 7, "b"); err != nil || api.last() != inst {
		t.Fatalf("UpdatePullRequestComment installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if _, err := client.ListReviews(ctx, "o/r", 1); err != nil || api.last() != inst {
		t.Fatalf("ListReviews installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if _, err := client.SubmitReview(ctx, "o/r", 1, ReviewInput{Event: "COMMENT"}); err != nil || api.last() != inst {
		t.Fatalf("SubmitReview installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if err := client.CreateCommitStatus(ctx, "o/r", "sha", "success", "ctx", "d", "u"); err != nil || api.last() != inst {
		t.Fatalf("CreateCommitStatus installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
	if err := client.GetRepository(ctx, "o/r"); err != nil || api.last() != inst {
		t.Fatalf("GetRepository installationID = %d, want %d (err=%v)", api.last(), inst, err)
	}
}

func TestGitHubReviewClientPassesZeroInstallationID(t *testing.T) {
	// 仓库级前置检查用 installationID=0：「先解析安装再查仓库」的既有语义
	// 在底层实现，适配器不得自行解析（那会多一次调用、改变请求序列）。
	api := &recordingAPI{}
	client := NewGitHubReviewClient(api, 0)
	if err := client.GetRepository(context.Background(), "o/r"); err != nil || api.last() != 0 {
		t.Fatalf("GetRepository installationID = %d, want 0 (err=%v)", api.last(), err)
	}
}

func TestGitHubReviewClientConvertsPullRequest(t *testing.T) {
	mergedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	closedAt := mergedAt.Add(time.Hour)
	updatedAt := closedAt.Add(time.Hour)
	api := &recordingAPI{pr: &githubapi.PullRequest{
		RepoFullName: "base/repo", Number: 9, Title: "t", Body: "b", HTMLURL: "u",
		State: "open", Draft: true, Merged: true, MergedAt: &mergedAt, ClosedAt: &closedAt,
		HeadSHA: "sha", HeadRef: "feature", HeadRepoFullName: "fork/other",
		BaseRef: "main", AuthorLogin: "alice",
		UpdatedAt: &updatedAt,
	}}
	pr, err := NewGitHubReviewClient(api, 1).GetPullRequest(context.Background(), "o/r", 9)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	want := PullRequest{
		RepoFullName: "base/repo", Number: 9, Title: "t", Body: "b", HTMLURL: "u",
		State: "open", Draft: true, Merged: true, MergedAt: &mergedAt, ClosedAt: &closedAt,
		HeadSHA: "sha", HeadRef: "feature", HeadRepoFullName: "fork/other",
		BaseRef: "main", AuthorLogin: "alice",
		UpdatedAt: &updatedAt,
	}
	if *pr != want {
		t.Fatalf("pr = %+v, want %+v", *pr, want)
	}
}

func TestGitHubReviewClientConvertsListAndSubmitTypes(t *testing.T) {
	at := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	reply := int64(55)
	api := &recordingAPI{
		reviewComments: []githubapi.ReviewComment{{ID: 1, User: "u", Body: "b", InReplyToID: &reply, Path: "a.go", CreatedAt: at}},
		issueComments:  []githubapi.IssueComment{{ID: 2, User: "u2", Body: "b2", CreatedAt: at}},
		reviews: []githubapi.ReviewSummary{
			{ID: 3, CommitID: "sha", Body: "rb", State: "COMMENT", CreatedAt: at},
			// PENDING 未提交草稿对读者不可见，「已提交」契约内不回传。
			{ID: 4, CommitID: "sha", Body: "draft", State: "PENDING", CreatedAt: at},
		},
		foundID: 8, found: true, createdID: 9, reviewID: 10,
	}
	client := NewGitHubReviewClient(api, 1)
	ctx := context.Background()

	rcs, err := client.ListReviewComments(ctx, "o/r", 1)
	if err != nil || len(rcs) != 1 || rcs[0].ID != 1 || rcs[0].User != "u" || rcs[0].Body != "b" ||
		rcs[0].InReplyToID == nil || *rcs[0].InReplyToID != 55 || rcs[0].Path != "a.go" || !rcs[0].CreatedAt.Equal(at) {
		t.Fatalf("ReviewComment 转换不完整: %+v (err=%v)", rcs, err)
	}
	ics, err := client.ListIssueCommentsForPR(ctx, "o/r", 1)
	if err != nil || len(ics) != 1 || ics[0].ID != 2 || ics[0].User != "u2" || ics[0].Body != "b2" || !ics[0].CreatedAt.Equal(at) {
		t.Fatalf("IssueComment 转换不完整: %+v (err=%v)", ics, err)
	}
	revs, err := client.ListReviews(ctx, "o/r", 1)
	if err != nil || len(revs) != 1 || revs[0].ID != 3 || revs[0].CommitID != "sha" ||
		revs[0].Body != "rb" || revs[0].State != "COMMENT" || !revs[0].CreatedAt.Equal(at) {
		t.Fatalf("ReviewSummary 转换不完整: %+v (err=%v)", revs, err)
	}
	if id, found, err := client.FindPullRequestComment(ctx, "o/r", 1, "m"); err != nil || id != 8 || !found {
		t.Fatalf("FindPullRequestComment = (%d,%v,%v), want (8,true,nil)", id, found, err)
	}
	if id, err := client.CreatePullRequestComment(ctx, "o/r", 1, "nb"); err != nil || id != 9 {
		t.Fatalf("CreatePullRequestComment = (%d,%v), want (9,nil)", id, err)
	}
	in := ReviewInput{Event: "COMMENT", Body: "sum", CommitID: "sha", Comments: []ReviewInlineComment{
		{Path: "a.go", Line: 5, Side: "RIGHT", Body: "c1"},
	}}
	if id, err := client.SubmitReview(ctx, "o/r", 1, in); err != nil || id != 10 {
		t.Fatalf("SubmitReview = (%d,%v), want (10,nil)", id, err)
	}
	if api.reviewInput.Event != "COMMENT" || api.reviewInput.Body != "sum" || api.reviewInput.CommitID != "sha" ||
		len(api.reviewInput.Comments) != 1 || api.reviewInput.Comments[0] != (githubapi.ReviewInlineComment{
		Path: "a.go", Line: 5, Side: "RIGHT", Body: "c1",
	}) {
		t.Fatalf("ReviewInput 转换不完整: %+v", api.reviewInput)
	}
	if err := client.UpdatePullRequestComment(ctx, "o/r", 7, "nb"); err != nil || api.updatedBody != "nb" {
		t.Fatalf("UpdatePullRequestComment body = %q (err=%v)", api.updatedBody, err)
	}
	if err := client.CreateCommitStatus(ctx, "o/r", "sha", "success", "ctx", "d", "u"); err != nil ||
		api.status != (statusCall{repo: "o/r", sha: "sha", state: "success", statusContext: "ctx", description: "d", targetURL: "u"}) {
		t.Fatalf("CreateCommitStatus 入参不完整: %+v (err=%v)", api.status, err)
	}
}

// 树截断归一（r4 P2）：githubapi.ErrTreeTruncated 经适配器归一为
// forge.ErrTreeTruncated，service 据此按「不可判定」放行而非 50200。
func TestGitHubReviewClientTreeTruncated(t *testing.T) {
	api := &recordingAPI{pathErr: githubapi.ErrTreeTruncated}
	client := NewGitHubReviewClient(api, 1)
	exists, err := client.PathExistsAtRef(context.Background(), "o/r", "x/y.go", "sha")
	if exists || !errors.Is(err, ErrTreeTruncated) {
		t.Fatalf("truncated = (%v, %v), want (false, ErrTreeTruncated)", exists, err)
	}
	// 非截断错误原样透传，不误归一。
	api.pathErr = errors.New("boom")
	if _, err := client.PathExistsAtRef(context.Background(), "o/r", "x/y.go", "sha"); errors.Is(err, ErrTreeTruncated) {
		t.Fatalf("普通错误不应归一为 ErrTreeTruncated, got %v", err)
	}
}

func TestGitHubReviewClientErrorPassthrough(t *testing.T) {
	// 适配器不包装错误：githubapi.StatusError 原样透出，forge 判定函数按状态
	// 码结构识别——GitHub 行为（错误文本、errors.As 链）不变。
	cases := []struct {
		name string
		err  error
		nf   bool
		fb   bool
		tl   bool
	}{
		{"404", &githubapi.StatusError{StatusCode: 404, Method: "PATCH", URL: "/x", Body: "nf"}, true, false, false},
		{"403", &githubapi.StatusError{StatusCode: 403, Method: "GET", URL: "/y", Body: "f"}, false, true, false},
		{"too_large", &githubapi.StatusError{StatusCode: 406, Method: "GET", URL: "/z",
			Body: `{"errors":[{"code":"too_large"}]}`}, false, false, true},
		{"网络错误", errors.New("dial tcp: refused"), false, false, false},
	}
	for _, c := range cases {
		api := &recordingAPI{repoErr: c.err}
		client := NewGitHubReviewClient(api, 1)
		err := client.GetRepository(context.Background(), "o/r")
		if !errors.Is(err, c.err) {
			t.Fatalf("%s: 错误应原样透传, got %v", c.name, err)
		}
		if got := IsNotFound(err); got != c.nf {
			t.Fatalf("%s: IsNotFound = %v, want %v", c.name, got, c.nf)
		}
		if got := IsForbidden(err); got != c.fb {
			t.Fatalf("%s: IsForbidden = %v, want %v", c.name, got, c.fb)
		}
		if got := IsDiffTooLarge(err); got != c.tl {
			t.Fatalf("%s: IsDiffTooLarge = %v, want %v", c.name, got, c.tl)
		}
	}
}

func TestForgeStatusErrorRecognized(t *testing.T) {
	// forge.StatusError 是 Forgejo 适配器（任务 3）的错误形态：同一组判定
	// 函数按状态码结构识别，类型身份不参与。
	nf := &StatusError{StatusCode: 404, Method: "GET", URL: "/f", Body: "nf"}
	if !IsNotFound(nf) || IsForbidden(nf) {
		t.Fatal("forge.StatusError 404 应被 IsNotFound 识别")
	}
	fb := &StatusError{StatusCode: 403}
	if !IsForbidden(fb) || IsNotFound(fb) {
		t.Fatal("forge.StatusError 403 应被 IsForbidden 识别")
	}
	tl := &StatusError{StatusCode: 406, Method: "GET", URL: "/d",
		Body: `{"errors":[{"code":"too_large"}]}`}
	if !IsDiffTooLarge(tl) || !IsDiffTooLarge(fmt.Errorf("wrap: %w", tl)) {
		t.Fatal("forge.StatusError too_large 应被 IsDiffTooLarge 识别（含链中）")
	}
	if !IsForbidden(fmt.Errorf("wrap: %w", ErrForbidden)) {
		t.Fatal("ErrForbidden 哨兵应被 IsForbidden 识别")
	}
}

func TestGitHubReviewClientBearer(t *testing.T) {
	// 底层有凭证面：委派并带上收编的 installationID；没有：返回空串（匿名）。
	bAPI := &withBearer{recordingAPI: &recordingAPI{}, token: "tok-1"}
	client := NewGitHubReviewClient(bAPI, 77)
	tok, err := client.Bearer(context.Background())
	if err != nil || tok != "tok-1" {
		t.Fatalf("Bearer = (%q,%v), want (tok-1,nil)", tok, err)
	}
	if bAPI.last() != 77 {
		t.Fatalf("Bearer installationID = %d, want 77", bAPI.last())
	}

	plain := NewGitHubReviewClient(&recordingAPI{}, 77)
	tok, err = plain.Bearer(context.Background())
	if err != nil || tok != "" {
		t.Fatalf("无凭证面 Bearer = (%q,%v), want (\"\",nil)", tok, err)
	}
}

func TestGitHubReviewClientKind(t *testing.T) {
	if got := NewGitHubReviewClient(&recordingAPI{}, 1).Kind(); got != KindGitHub {
		t.Fatalf("Kind = %q, want %q", got, KindGitHub)
	}
}
