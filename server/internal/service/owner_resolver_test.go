package service

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func startResolvedPipeline(t *testing.T, resolver selflog.OwnerResolver) (*selflog.Pipeline, *selflog.Store) {
	t.Helper()
	store, err := selflog.OpenStore(filepath.Join(t.TempDir(), "self_logs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	p, err := selflog.Start(selflog.Options{
		Sink:          store,
		Stdout:        slog.NewTextHandler(io.Discard, nil),
		FlushInterval: 5 * time.Millisecond,
		MinLevel:      slog.LevelDebug,
	})
	if err != nil {
		t.Fatalf("start pipeline: %v", err)
	}
	p.SetOwnerResolver(resolver)
	t.Cleanup(p.Close)
	return p, store
}

func visibleMessages(t *testing.T, store *selflog.Store, userID string) []string {
	t.Helper()
	page, err := store.Query(context.Background(), selflog.Filter{VisibleUserID: userID}, "", 50, true)
	if err != nil {
		t.Fatalf("query %s: %v", userID, err)
	}
	out := make([]string, 0, len(page.Items))
	for _, e := range page.Items {
		out = append(out, e.Message)
	}
	return out
}

func hasMessage(msgs []string, want string) bool {
	for _, m := range msgs {
		if m == want {
			return true
		}
	}
	return false
}

func TestPipelineResolvesProjectOwnerAndHidesFromOthers(t *testing.T) {
	svc := setupTestServices(t)
	alice := createTestUser(t, svc.db, "alice-owner")
	bob := createTestUser(t, svc.db, "bob-viewer")
	project := createTestProject(t, svc.db, alice.ID)

	p, store := startResolvedPipeline(t, NewSelfLogOwnerResolver(svc.db))
	slog.New(p.Handler()).Info("仅项目", slog.String("project_id", project.ID))
	p.Close()

	aliceMsgs := visibleMessages(t, store, alice.ID)
	bobMsgs := visibleMessages(t, store, bob.ID)
	if !hasMessage(aliceMsgs, "仅项目") {
		t.Fatalf("alice 应看到解析出所有者的行: %v", aliceMsgs)
	}
	if hasMessage(bobMsgs, "仅项目") {
		t.Fatalf("bob 不应看到 alice 项目的日志: %v", bobMsgs)
	}
	page, err := store.Query(context.Background(), selflog.Filter{}, "", 10, true)
	if err != nil {
		t.Fatalf("query all: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].UserID != alice.ID {
		t.Fatalf("入库应写上解析出的 user_id: %+v", page.Items)
	}
}

func TestPipelineResolveFailureHiddenFromBoth(t *testing.T) {
	svc := setupTestServices(t)
	alice := createTestUser(t, svc.db, "alice-miss")
	bob := createTestUser(t, svc.db, "bob-miss")

	p, store := startResolvedPipeline(t, NewSelfLogOwnerResolver(svc.db))
	slog.New(p.Handler()).Info("查无项目", slog.String("project_id", uuid.NewString()))
	p.Close()

	if msgs := visibleMessages(t, store, alice.ID); hasMessage(msgs, "查无项目") {
		t.Fatalf("解析失败不应让 alice 看见: %v", msgs)
	}
	if msgs := visibleMessages(t, store, bob.ID); hasMessage(msgs, "查无项目") {
		t.Fatalf("解析失败不应让 bob 看见: %v", msgs)
	}
}

func TestPipelineConflictingKeysStayOwnerless(t *testing.T) {
	svc := setupTestServices(t)
	alice := createTestUser(t, svc.db, "alice-conflict")
	bob := createTestUser(t, svc.db, "bob-conflict")
	aliceProj := createTestProject(t, svc.db, alice.ID)
	bobRun := createAutomationRunForUser(t, svc.db, bob.ID)

	p, store := startResolvedPipeline(t, NewSelfLogOwnerResolver(svc.db))
	slog.New(p.Handler()).Info("两键冲突",
		slog.String("project_id", aliceProj.ID),
		slog.String("run_id", bobRun.ID),
	)
	p.Close()

	if msgs := visibleMessages(t, store, alice.ID); hasMessage(msgs, "两键冲突") {
		t.Fatalf("冲突行不应判给 alice: %v", msgs)
	}
	if msgs := visibleMessages(t, store, bob.ID); hasMessage(msgs, "两键冲突") {
		t.Fatalf("冲突行不应判给 bob: %v", msgs)
	}
	page, err := store.Query(context.Background(), selflog.Filter{}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].UserID != "" {
		t.Fatalf("冲突行应保持无主: %+v", page.Items)
	}
}

func TestPipelineExplicitUserIDNotOverwritten(t *testing.T) {
	svc := setupTestServices(t)
	alice := createTestUser(t, svc.db, "alice-explicit")
	bob := createTestUser(t, svc.db, "bob-explicit")
	bobProj := createTestProject(t, svc.db, bob.ID)

	p, store := startResolvedPipeline(t, NewSelfLogOwnerResolver(svc.db))
	slog.New(p.Handler()).Info("显式归属",
		slog.String("user_id", alice.ID),
		slog.String("project_id", bobProj.ID),
	)
	p.Close()

	page, err := store.Query(context.Background(), selflog.Filter{}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].UserID != alice.ID {
		t.Fatalf("显式 user_id 不得被解析器改写: %+v", page.Items)
	}
	if !hasMessage(visibleMessages(t, store, alice.ID), "显式归属") {
		t.Fatalf("alice 应看到自己的显式归属行")
	}
	if hasMessage(visibleMessages(t, store, bob.ID), "显式归属") {
		t.Fatalf("bob 不应因 project_id 抢走显式归属行")
	}
}

func TestPipelineMisMarkedSystemScopeWithAssociationIsNotPublic(t *testing.T) {
	svc := setupTestServices(t)
	alice := createTestUser(t, svc.db, "alice-misflag")
	bob := createTestUser(t, svc.db, "bob-misflag")
	project := createTestProject(t, svc.db, alice.ID)

	p, store := startResolvedPipeline(t, NewSelfLogOwnerResolver(svc.db))
	slog.New(p.Handler()).Info("误标系统",
		slog.Bool(selflog.AttrSystemScope, true),
		slog.String("project_id", project.ID),
	)
	p.Close()

	page, err := store.Query(context.Background(), selflog.Filter{}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	if page.Items[0].SystemScope {
		t.Fatalf("带关联列的行即使误标也不得以系统行落库: %+v", page.Items[0])
	}
	if hasMessage(visibleMessages(t, store, bob.ID), "误标系统") {
		t.Fatalf("误标系统的业务行不得对 bob 公开")
	}
	if !hasMessage(visibleMessages(t, store, alice.ID), "误标系统") {
		t.Fatalf("清除误标后应解析给 alice: %v", visibleMessages(t, store, alice.ID))
	}
}

func createAutomationRunForUser(t *testing.T, db *gorm.DB, userID string) *model.AutomationRun {
	t.Helper()
	now := time.Now().UTC()
	env := &model.T3Environment{
		ID:                   uuid.NewString(),
		UserID:               userID,
		Name:                 "env-" + uuid.NewString()[:8],
		Address:              "https://t3.example.com",
		BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt:      now.Add(time.Hour),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := db.Create(env).Error; err != nil {
		t.Fatalf("create env: %v", err)
	}
	a := &model.Automation{
		ID:             uuid.NewString(),
		UserID:         userID,
		Name:           "run-owner",
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
		EnvironmentID:  env.ID,
		T3ProjectID:    "t3-proj",
		TriggerType:    model.AutomationTriggerIssueCreated,
		PromptTemplate: "x",
		RuntimeMode:    "full-access",
		TimeoutSeconds: 3600,
		PostAction:     model.AutomationPostNone,
	}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("create automation: %v", err)
	}
	run := &model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             userID,
		AutomationID:       a.ID,
		TriggerType:        a.TriggerType,
		TriggerKey:         "event:" + uuid.NewString(),
		TriggeredAt:        now,
		ConfigSnapshotJSON: `{}`,
		EnvironmentID:      env.ID,
		T3ProjectID:        a.T3ProjectID,
		ThreadID:           uuid.NewString(),
		LaunchCommandID:    uuid.NewString(),
		InitialMessageID:   uuid.NewString(),
		Status:             model.RunStatusPendingRun,
		DeadlineAt:         now.Add(time.Hour),
		PostState:          model.AutomationPostStateNone,
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	return run
}
