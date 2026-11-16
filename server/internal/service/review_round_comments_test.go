package service

import (
	"context"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-233：顶层评论每轮新发——开轮推进轮次时清零上一轮的评论 id；同 head
// 非 manual/comment 沿用原轮次时不清，同轮重试仍按账本 id 幂等跳过。
func TestOpenRoundClearsCommentIDsOnRoundChange(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	combos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-rc")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.CodeCombinationsJSON = combos
	})
	var settings model.ProjectReviewSettings
	if err := e.svc.db.First(&settings, "project_id = ?", e.project.ID).Error; err != nil {
		t.Fatalf("load settings: %v", err)
	}

	round1, err := e.review.openRoundTx(e.svc.db, e.project, &settings,
		repo, 60, "sha-r1", "push", "t", "u", "main", "feature", 1, nil, now)
	if err != nil {
		t.Fatalf("openRound 1: %v", err)
	}
	if round1 != 1 {
		t.Fatalf("round = %d, want 1", round1)
	}
	// 模拟上一轮的评论已发出（账本记了两个 id）。
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 60).
		Updates(map[string]any{"code_comment_id": 101, "visual_comment_id": 202}).Error; err != nil {
		t.Fatalf("seed comment ids: %v", err)
	}

	// 新 head push：轮次推进 → 两个评论 id 都清零。
	round2, err := e.review.openRoundTx(e.svc.db, e.project, &settings,
		repo, 60, "sha-r2", "push", "t", "u", "main", "feature", 1, nil, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("openRound 2: %v", err)
	}
	if round2 != 2 {
		t.Fatalf("round = %d, want 2", round2)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 60", repo)
	if ledger.CodeCommentID != 0 || ledger.VisualCommentID != 0 {
		t.Fatalf("轮次推进后评论 id 应清零: code=%d visual=%d", ledger.CodeCommentID, ledger.VisualCommentID)
	}

	// 恢复 id 后同 head 再受理（非 manual/comment）：沿用原轮次，id 不清。
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 60).
		Updates(map[string]any{"code_comment_id": 303}).Error; err != nil {
		t.Fatalf("seed code id: %v", err)
	}
	roundReuse, err := e.review.openRoundTx(e.svc.db, e.project, &settings,
		repo, 60, "sha-r2", "push", "t", "u", "main", "feature", 1, nil, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("openRound reuse: %v", err)
	}
	if roundReuse != 2 {
		t.Fatalf("round = %d, want 2（同 head 沿用原轮次）", roundReuse)
	}
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 60", repo)
	if ledger.CodeCommentID != 303 {
		t.Fatalf("同轮沿用不清评论 id: code=%d", ledger.CodeCommentID)
	}

	// manual 恒开新轮：id 再次清零。
	if _, err := e.review.openRoundTx(e.svc.db, e.project, &settings,
		repo, 60, "sha-r2", "manual", "t", "u", "main", "feature", 1, nil, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("openRound manual: %v", err)
	}
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 60", repo)
	if ledger.CodeCommentID != 0 {
		t.Fatalf("manual 新轮后评论 id 应清零: code=%d", ledger.CodeCommentID)
	}
}

// INT-233 审查 R1：Create 请求在途时开新轮（手动重跑/评论唤醒），旧响应返回
// 后 recordCommentID 不得把旧轮 id 写进新轮账本——否则新轮按 id 短路跳过
// 发布，本轮结论永久漏发。
func TestRecordCommentIDGuardedAgainstRoundAdvance(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	const marker = reviewCodeCommentMarker
	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 61, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-a", RoundNumber: 1, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	// Create 成功的同时模拟旧轮请求在途期间开了新轮（openRoundTx 的写法）。
	race := &roundRaceStubClient{onCreate: func() {
		if err := e.svc.db.Model(&model.ReviewPullRequest{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 61).
			Updates(map[string]any{"head_sha": "sha-b", "round_number": 2,
				"code_comment_id": 0, "visual_comment_id": 0}).Error; err != nil {
			t.Errorf("advance round: %v", err)
		}
	}}
	if err := e.review.postRoundComment(context.Background(), race, ledger,
		marker, marker+"\n正文", "code_comment_id", 0); err != nil {
		t.Fatalf("postRoundComment: %v", err)
	}
	var after model.ReviewPullRequest
	e.svc.db.First(&after, "repo_full_name = ? AND pr_number = 61", repo)
	if after.CodeCommentID != 0 {
		t.Fatalf("旧响应的 id 不得写进新轮账本, code_comment_id=%d", after.CodeCommentID)
	}

	// 对照：round 未推进时照常写回。
	same := &roundRaceStubClient{}
	ledger.HeadSHA = "sha-b"
	ledger.RoundNumber = 2
	if err := e.review.postRoundComment(context.Background(), same, ledger,
		marker, marker+"\n正文", "code_comment_id", 0); err != nil {
		t.Fatalf("postRoundComment same round: %v", err)
	}
	e.svc.db.First(&after, "repo_full_name = ? AND pr_number = 61", repo)
	if after.CodeCommentID == 0 || after.HeadSHA != "sha-b" || after.RoundNumber != 2 {
		t.Fatalf("同轮 Create 的 id 应写回: id=%d head=%s round=%d",
			after.CodeCommentID, after.HeadSHA, after.RoundNumber)
	}
}

// roundRaceStubClient 在 Create 返回前执行钩子（模拟网络请求在途时账本被推进）。
type roundRaceStubClient struct {
	forge.ReviewClient
	onCreate func()
}

func (c *roundRaceStubClient) FindPullRequestComment(context.Context, string, int, string) (int64, bool, error) {
	return 0, false, nil
}

func (c *roundRaceStubClient) CreatePullRequestComment(ctx context.Context, repo string, number int, body string) (int64, error) {
	if c.onCreate != nil {
		c.onCreate()
	}
	return 1001, nil
}
