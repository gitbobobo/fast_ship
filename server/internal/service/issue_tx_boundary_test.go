package service

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	gormlogger "gorm.io/gorm/logger"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
)

// 资产独立入口包事务后的原子性：先挂接成功、随后删除失败时，挂接也要回滚。
func TestIssueServiceReconcileIssueAssets_RollsBackAttachWhenDeleteFails(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	attachID := uuid.NewString()
	deleteID := uuid.NewString()
	now := time.Now().UTC()
	for _, asset := range []model.IssueAsset{
		{ID: attachID, IssueID: issue.ID, FileName: "keep.png", FilePath: "keep/path.png", MimeType: "image/png", FileSize: 1, Status: model.IssueAssetStatusPending, CreatedByUserID: user.ID, CreatedAt: now},
		{ID: deleteID, IssueID: issue.ID, FileName: "drop.png", FilePath: "drop/path.png", MimeType: "image/png", FileSize: 1, Status: model.IssueAssetStatusPending, CreatedByUserID: user.ID, CreatedAt: now},
	} {
		if err := svc.db.Create(&asset).Error; err != nil {
			t.Fatalf("seed issue asset: %v", err)
		}
	}

	if err := svc.db.Exec("CREATE TRIGGER block_issue_asset_delete BEFORE DELETE ON issue_assets BEGIN SELECT RAISE(ABORT, 'delete blocked'); END;").Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// 文件落盘后才能断言「失败路径不误删文件」。
	for _, path := range []string{"keep/path.png", "drop/path.png"} {
		if err := svc.storage.Save(path, strings.NewReader("asset")); err != nil {
			t.Fatalf("save %s: %v", path, err)
		}
	}

	body := fmt.Sprintf("引用图片\n\n![keep](/api/issues/assets/%s/content)", attachID)
	// reconcileIssueAssets 原样返回事务里的错误，从报错文本能钉住是 DELETE 触发器失败。
	err := svc.issueService.reconcileIssueAssets(issue.ID, body)
	if err == nil || !strings.Contains(err.Error(), "delete blocked") {
		t.Fatalf("expected delete-trigger failure, got %v", err)
	}
	for _, path := range []string{"keep/path.png", "drop/path.png"} {
		exists, err := svc.storage.Exists(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !exists {
			t.Fatalf("expected file %s to survive failed reconcile", path)
		}
	}

	assets, err := svc.issueAssetRepo.ListByIssueID(issue.ID)
	if err != nil {
		t.Fatalf("list issue assets: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("expected both assets to remain after rollback, got %d", len(assets))
	}
	for _, asset := range assets {
		if asset.ID == attachID && asset.Status != model.IssueAssetStatusPending {
			t.Fatalf("expected attach to roll back to pending, got %q", asset.Status)
		}
	}
}

// 镜像 upsert 关闭路径：落库成功但删推荐失败时，issue 必须保持同步前状态。
func TestIssueMirrorUpsertGitHubIssue_ClosedPathRollsBackWhenRecommendationDeleteFails(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert recommendation: %v", err)
	}

	if err := ts.db.Exec("CREATE TRIGGER block_rec_delete BEFORE DELETE ON issue_recommendations BEGIN SELECT RAISE(ABORT, 'delete blocked'); END;").Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// upsertGitHubIssue 把错误包成 errs.ErrInternal，看不到底层；改用 GORM 错误日志
	// 证明失败点确是 issue_recommendations 上的 DELETE（生产与测试共用同一个 *gorm.DB，
	// 换 Logger 即生效）。
	var logBuf bytes.Buffer
	ts.db.Logger = gormlogger.New(log.New(&logBuf, "", 0), gormlogger.Config{LogLevel: gormlogger.Error})

	now := time.Now().UTC()
	item := &ghclient.Issue{
		Issue: gh.Issue{
			ID:        gh.Int64(1001),
			Number:    gh.Int(42),
			State:     gh.String("closed"),
			Title:     gh.String("同步后的标题"),
			CreatedAt: &gh.Timestamp{Time: now.Add(-time.Hour)},
			UpdatedAt: &gh.Timestamp{Time: now},
			ClosedAt:  &gh.Timestamp{Time: now},
			User:      &gh.User{Login: gh.String("alice")},
		},
	}
	if _, err := ts.issueService.mirror.upsertGitHubIssue(issue.ProjectID, item); err != errs.ErrInternal {
		t.Fatalf("expected internal error, got %v", err)
	}

	var got model.Issue
	if err := ts.db.Where("id = ?", issue.ID).First(&got).Error; err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if got.Title != issue.Title {
		t.Fatalf("expected issue title to roll back to %q, got %q", issue.Title, got.Title)
	}
	if got.State != model.IssueStateOpen {
		t.Fatalf("expected issue state to stay open, got %q", got.State)
	}
	if _, err := ts.recRepo.Get(issue.ID); err != nil {
		t.Fatalf("expected recommendation to remain after rollback: %v", err)
	}

	// 错误日志里应同时出现 issue_recommendations 的 DELETE 语句和触发器报错文本。
	logText := logBuf.String()
	for _, want := range []string{"DELETE FROM", "issue_recommendations", "delete blocked"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected GORM error log to contain %q, got: %s", want, logText)
		}
	}
}
