package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupQuotaAccountTestDB(t *testing.T) (*gorm.DB, *QuotaAccountRepository) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.QuotaAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewQuotaAccountRepository(db)
}

func seedQuotaAccountUser(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	if err := db.Create(&model.User{ID: id, Username: "u-" + id, Email: id + "@x", PasswordHash: "x"}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func seedQuotaAccount(t *testing.T, db *gorm.DB, userID string, opts ...func(*model.QuotaAccount)) *model.QuotaAccount {
	t.Helper()
	account := &model.QuotaAccount{
		ID: uuid.NewString(), UserID: userID, Provider: "fake", Label: "acct",
		CredentialsEncrypted: []byte("enc"), CredentialHint: "…ab12",
		Status: model.QuotaStatusPending, SnapshotJSON: "",
	}
	for _, opt := range opts {
		opt(account)
	}
	if err := db.Create(account).Error; err != nil {
		t.Fatalf("seed quota account: %v", err)
	}
	return account
}

// 用户隔离：FindByID/ListByUserID/UpdateLabel/UpdateCredentials/Delete 全部带
// user_id 条件，他人行不可见也不可改。
func TestQuotaAccountRepository_UserIsolation(t *testing.T) {
	db, repo := setupQuotaAccountTestDB(t)
	seedQuotaAccountUser(t, db, "owner")
	seedQuotaAccountUser(t, db, "stranger")
	mine := seedQuotaAccount(t, db, "owner")
	foreign := seedQuotaAccount(t, db, "stranger")

	if _, err := repo.FindByID(mine.ID, "stranger"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign FindByID: %v", err)
	}
	items, err := repo.ListByUserID("stranger")
	if err != nil || len(items) != 1 || items[0].ID != foreign.ID {
		t.Fatalf("list isolation: %+v err=%v", items, err)
	}
	if err := repo.UpdateLabel(mine.ID, "stranger", "hijack"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign UpdateLabel: %v", err)
	}
	if err := repo.UpdateCredentials(mine.ID, "stranger", []byte("x"), "…x"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign UpdateCredentials: %v", err)
	}
	if err := repo.Delete(mine.ID, "stranger"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("foreign Delete: %v", err)
	}
	var stillThere model.QuotaAccount
	if err := db.First(&stillThere, "id = ?", mine.ID).Error; err != nil || stillThere.Label != "acct" {
		t.Fatalf("foreign ops must not touch the row: %+v err=%v", stillThere, err)
	}
}

// ListDue 选 next_retry_at 为空或到期的行；EarliestNextRetryAt 返回最早未来值。
func TestQuotaAccountRepository_ListDue(t *testing.T) {
	db, repo := setupQuotaAccountTestDB(t)
	seedQuotaAccountUser(t, db, "u1")
	now := time.Now().UTC()

	pending := seedQuotaAccount(t, db, "u1")
	past := now.Add(-time.Minute)
	overdue := seedQuotaAccount(t, db, "u1", func(a *model.QuotaAccount) { a.NextRetryAt = &past })
	future := now.Add(5 * time.Minute)
	seedQuotaAccount(t, db, "u1", func(a *model.QuotaAccount) { a.NextRetryAt = &future })

	due, err := repo.ListDue(now)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	ids := map[string]bool{}
	for _, a := range due {
		ids[a.ID] = true
	}
	if len(ids) != 2 || !ids[pending.ID] || !ids[overdue.ID] {
		t.Fatalf("expected pending + overdue due, got %+v", ids)
	}

	earliest, err := repo.EarliestNextRetryAt(now)
	if err != nil || earliest == nil || earliest.Sub(future).Abs() > time.Second {
		t.Fatalf("earliest = %v err=%v, want ~%v", earliest, err, future)
	}
}

// SaveFetchSuccess 写快照列并清退避；SaveFetchFailure 只写失败列，不碰快照。
func TestQuotaAccountRepository_SaveFetchResult(t *testing.T) {
	db, repo := setupQuotaAccountTestDB(t)
	seedQuotaAccountUser(t, db, "u1")
	account := seedQuotaAccount(t, db, "u1")
	now := time.Now().UTC().Truncate(time.Second)

	retry := now.Add(3 * time.Minute)
	if err := repo.SaveFetchSuccess(account.ID, `{"plan_name":"Pro"}`, "Pro", now, &retry); err != nil {
		t.Fatalf("save success: %v", err)
	}
	var stored model.QuotaAccount
	if err := db.First(&stored, "id = ?", account.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != model.QuotaStatusOK || stored.PlanName != "Pro" || stored.SnapshotJSON == "" ||
		stored.LastFetchedAt == nil || stored.LastSuccessAt == nil || stored.NextRetryAt == nil || stored.FailureCount != 0 {
		t.Fatalf("success write mismatch: %+v", stored)
	}

	failRetry := now.Add(2 * time.Minute)
	if err := repo.SaveFetchFailure(account.ID, model.QuotaStatusProviderError, "接口异常", 1, failRetry); err != nil {
		t.Fatalf("save failure: %v", err)
	}
	if err := db.First(&stored, "id = ?", account.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != model.QuotaStatusProviderError || stored.LastError != "接口异常" || stored.FailureCount != 1 {
		t.Fatalf("failure write mismatch: %+v", stored)
	}
	if stored.SnapshotJSON == "" || stored.PlanName != "Pro" || stored.LastSuccessAt == nil {
		t.Fatalf("failure must preserve success snapshot: %+v", stored)
	}
	if stored.NextRetryAt == nil || stored.NextRetryAt.Sub(failRetry).Abs() > time.Second {
		t.Fatalf("next_retry_at = %v, want ~%v", stored.NextRetryAt, failRetry)
	}
}
