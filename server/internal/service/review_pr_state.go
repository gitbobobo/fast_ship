package service

import "github.com/godbobo/fast_ship/server/internal/forge"

// PR 实况关卡（INT-147 第二部分）：重投/迟到的 webhook 只重放原始 payload
// （PR 仍写着 open、updated_at/head 都是旧值），任何「是否该继续审」的判定
// 都必须以 GetPullRequest 实时读到的 State 为准。

// prReviewable PR 实况是否仍可审查：只有 State=="open" 值得开轮、建线程或
// 写 GitHub 产出；nil 或 closed（含 merged）一律不可审。
func prReviewable(pr *forge.PullRequest) bool {
	return pr != nil && pr.State == "open"
}
