package forge

import "time"

// Forgejo v1 REST 的响应字段面（INT-167 任务 3）：只取审查链路用到的字段，
// JSON 名与 Forgejo/Gitea swagger 对齐。时间戳统一 RFC3339，可空字段用指针。

// forgejoUser PR/评论/review 的作者面。
type forgejoUser struct {
	Login string `json:"login"`
}

// forgejoRepoRef head/base 的仓库面：fork head 与已删 fork（repo=null）是
// 轮询与 webhook 共同的忽略条件。
type forgejoRepoRef struct {
	FullName string `json:"full_name"`
	Fork     bool   `json:"fork"`
}

// forgejoPR GET /repos/{o}/{r}/pulls/{n} 与 open PR 枚举列表共用的元素形态。
type forgejoPR struct {
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	State    string     `json:"state"`
	Draft    bool       `json:"draft"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	ClosedAt *time.Time `json:"closed_at"`
	// UpdatedAt PR 最后更新戳：轮询把快照里的旧 head 带过来，受理端用它
	// 与已受理 head 的 pr_updated_at 比序，挡掉「读到后平台又推新 head」
	// 的过期快照（见 review_forgejo_poll）。
	UpdatedAt *time.Time `json:"updated_at"`
	HTMLURL   string     `json:"html_url"`
	Head      struct {
		Ref  string          `json:"ref"`
		SHA  string          `json:"sha"`
		Repo *forgejoRepoRef `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string          `json:"ref"`
		Repo *forgejoRepoRef `json:"repo"`
	} `json:"base"`
	User forgejoUser `json:"user"`
}

// toPullRequest 转中性类型：RepoFullName 取 base 仓库 canonical 名（与 GitHub
// 适配器同口径）；head 仓库面不进中性类型——fork 判定只发生在 Forgejo 侧
// （ListOpenPullRequests 已过滤 fork head）。
func (w *forgejoPR) toPullRequest() *PullRequest {
	out := &PullRequest{
		Number:      w.Number,
		Title:       w.Title,
		Body:        w.Body,
		HTMLURL:     w.HTMLURL,
		State:       w.State,
		Draft:       w.Draft,
		Merged:      w.Merged,
		MergedAt:    w.MergedAt,
		ClosedAt:    w.ClosedAt,
		HeadSHA:     w.Head.SHA,
		HeadRef:     w.Head.Ref,
		BaseRef:     w.Base.Ref,
		AuthorLogin: w.User.Login,
		UpdatedAt:   w.UpdatedAt,
	}
	if w.Base.Repo != nil {
		out.RepoFullName = w.Base.Repo.FullName
	}
	return out
}

// forgejoComment 行内评论（review comments）与顶层评论（issue comments）的
// 共同字段面：Forgejo 里 PR 与 issue 共用评论空间，两处响应同形。
type forgejoComment struct {
	ID        int64       `json:"id"`
	User      forgejoUser `json:"user"`
	Body      string      `json:"body"`
	Path      string      `json:"path"`
	CreatedAt time.Time   `json:"created_at"`
}

// forgejoReview PR review（GET .../pulls/{n}/reviews）。
type forgejoReview struct {
	ID          int64       `json:"id"`
	Body        string      `json:"body"`
	State       string      `json:"state"`
	CommitID    string      `json:"commit_id"`
	User        forgejoUser `json:"user"`
	SubmittedAt *time.Time  `json:"submitted_at"`
}

// forgejoSubmitReview POST .../pulls/{n}/reviews 的请求体。event ∈ COMMENT/
// APPROVED/REJECTED（Forgejo 对应 GitHub 的 COMMENT/APPROVE/REQUEST_CHANGES，
// 审查链路只用 COMMENT）；行内评论 new_position 是新侧行号（我们的 RIGHT
// side），old_position 是旧侧行号。
type forgejoSubmitReview struct {
	Event    string              `json:"event"`
	Body     string              `json:"body,omitempty"`
	CommitID string              `json:"commit_id,omitempty"`
	Comments []forgejoReviewLine `json:"comments,omitempty"`
}

// forgejoReviewLine 随 review 提交的行内评论：行号二选一，按调用方的 Side
// 映射（LEFT→old_position，RIGHT/缺省→new_position）。
type forgejoReviewLine struct {
	Path        string `json:"path"`
	Body        string `json:"body"`
	NewPosition int64  `json:"new_position,omitempty"`
	OldPosition int64  `json:"old_position,omitempty"`
}

// forgejoCommitStatus POST /repos/{o}/{r}/statuses/{sha} 的请求体。state ∈
// pending/success/failure/error，与 ReviewClient 语义一致。
type forgejoCommitStatus struct {
	State       string `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context"`
}
