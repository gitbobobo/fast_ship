package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestIssueServiceUpsertShipHook_PutPendingAndGet(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	commentBody := "已随 {version} 发出。"
	workflow := model.IssueWorkflowStatusDone
	hook, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody:    &commentBody,
		Close:          true,
		WorkflowStatus: &workflow,
	})
	if err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}
	if hook.Status != string(model.IssueShipHookStatusPending) {
		t.Fatalf("expected pending status, got %q", hook.Status)
	}
	if hook.CommentBody != commentBody || !hook.CloseEnabled || !hook.WorkflowEnabled || hook.WorkflowStatus != string(workflow) {
		t.Fatalf("unexpected hook payload: %+v", hook)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook == nil {
		t.Fatalf("expected ship_hook on issue")
	}
	if got.ShipHook.Status != string(model.IssueShipHookStatusPending) {
		t.Fatalf("expected pending ship_hook, got %+v", got.ShipHook)
	}
}

func TestIssueServiceUpsertShipHook_OverwritesFiredToPending(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	now := time.Now().UTC()
	ok := true
	if err := svc.db.Create(&model.IssueShipHook{
		IssueID:            issue.ID,
		ProjectID:          project.ID,
		Status:             model.IssueShipHookStatusFired,
		CommentEnabled:     true,
		CommentBody:        "old",
		CloseEnabled:       true,
		WorkflowEnabled:    true,
		WorkflowStatus:     model.IssueWorkflowStatusDone,
		FiredVersionID:     "ver-old",
		FiredVersionNumber: "1.0.0",
		FiredReleaseURL:    "https://example.com/1.0.0",
		FiredAt:            &now,
		CommentOK:          &ok,
		CreatedByUserID:    user.ID,
		UpdatedByUserID:    user.ID,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed fired hook: %v", err)
	}

	workflow := model.IssueWorkflowStatusTodo
	hook, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		WorkflowStatus: &workflow,
	})
	if err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}
	if hook.Status != string(model.IssueShipHookStatusPending) {
		t.Fatalf("expected pending after overwrite, got %q", hook.Status)
	}

	var stored model.IssueShipHook
	if err := svc.db.Where("issue_id = ?", issue.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored hook: %v", err)
	}
	if stored.Status != model.IssueShipHookStatusPending {
		t.Fatalf("expected stored pending, got %q", stored.Status)
	}
	if stored.FiredVersionID != "" || stored.CommentOK != nil {
		t.Fatalf("expected fired fields cleared, got %+v", stored)
	}
}

func TestIssueServiceUpsertShipHook_RejectsNoActions(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	_, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{})
	if err == nil {
		t.Fatalf("expected error for empty actions")
	}
	if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("expected invalid params, got %v", err)
	}
}

func TestIssueServiceUpsertShipHook_RejectsBlankComment(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	blank := "   "
	_, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody: &blank,
	})
	if err == nil {
		t.Fatalf("expected error for blank comment")
	}
}

func TestIssueServiceUpsertShipHook_RejectsCommentTooLong(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	longBody := strings.Repeat("a", maxShipHookCommentBodyLen+1)
	_, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody: &longBody,
	})
	if err == nil {
		t.Fatalf("expected error for long comment")
	}

	longRunes := strings.Repeat("汉", maxShipHookCommentBodyLen+1)
	_, err = svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody: &longRunes,
	})
	if err == nil {
		t.Fatalf("expected error for long rune comment")
	}
}

func TestIssueServiceUpsertShipHook_Allows2000ChineseRunes(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	body := strings.Repeat("汉", 2000)
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		CommentBody: &body,
	}); err != nil {
		t.Fatalf("expected 2000 runes to succeed, got %v", err)
	}
}

func TestIssueServiceUpsertShipHook_AllowsEmptyWorkflowStatus(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	empty := model.IssueWorkflowStatus("")
	hook, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		WorkflowStatus: &empty,
	})
	if err != nil {
		t.Fatalf("upsert ship hook with empty workflow: %v", err)
	}
	if !hook.WorkflowEnabled || hook.WorkflowStatus != "" {
		t.Fatalf("expected workflow enabled with empty status, got %+v", hook)
	}

	data, err := json.Marshal(hook)
	if err != nil {
		t.Fatalf("marshal hook: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal hook json: %v", err)
	}
	if _, ok := payload["workflow_status"]; !ok {
		t.Fatalf("expected workflow_status key in json, got %s", string(data))
	}
	if payload["workflow_status"] != "" {
		t.Fatalf("expected empty workflow_status value, got %v", payload["workflow_status"])
	}
	// 显式布尔与 workflow_status 即使为 false / 空串也要出现在 JSON 中。
	for _, key := range []string{"comment_enabled", "close_enabled"} {
		if value, ok := payload[key]; !ok || value != false {
			t.Fatalf("expected %s=false in json, got %s", key, string(data))
		}
	}
	if payload["workflow_enabled"] != true {
		t.Fatalf("expected workflow_enabled=true in json, got %s", string(data))
	}
}

func TestIssueServiceUpsertShipHook_AllowsClosedIssue(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID, func(i *model.Issue) {
		i.State = model.IssueStateClosed
	})

	workflow := model.IssueWorkflowStatusDone
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		WorkflowStatus: &workflow,
	}); err != nil {
		t.Fatalf("upsert on closed issue: %v", err)
	}
}

func TestIssueServiceDeleteShipHook_IsIdempotent(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	workflow := model.IssueWorkflowStatusDone
	if _, err := svc.issueService.UpsertShipHook(issue.ID, user.ID, UpsertShipHookRequest{
		WorkflowStatus: &workflow,
	}); err != nil {
		t.Fatalf("upsert ship hook: %v", err)
	}

	if err := svc.issueService.DeleteShipHook(issue.ID, user.ID); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := svc.issueService.DeleteShipHook(issue.ID, user.ID); err != nil {
		t.Fatalf("second delete should be idempotent: %v", err)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook != nil {
		t.Fatalf("expected no ship_hook after delete, got %+v", got.ShipHook)
	}
}

func TestIssueServiceList_IncludesShipHooksWithoutNPlusOne(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	issue1, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "First",
	})
	if err != nil {
		t.Fatalf("create issue1: %v", err)
	}
	issue2, err := svc.issueService.CreateInternalIssue(project.ID, user.ID, CreateInternalIssueRequest{
		Title: "Second",
	})
	if err != nil {
		t.Fatalf("create issue2: %v", err)
	}

	workflow := model.IssueWorkflowStatusDone
	for _, issueID := range []string{issue1.ID, issue2.ID} {
		if _, err := svc.issueService.UpsertShipHook(issueID, user.ID, UpsertShipHookRequest{
			WorkflowStatus: &workflow,
		}); err != nil {
			t.Fatalf("upsert ship hook for %s: %v", issueID, err)
		}
	}

	items, total, err := svc.issueService.List(project.ID, user.ID, IssueListFilters{}, 1, 20)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 issues, got total=%d len=%d", total, len(items))
	}

	found := 0
	for _, item := range items {
		if item.ShipHook == nil || item.ShipHook.Status != string(model.IssueShipHookStatusPending) {
			continue
		}
		found++
	}
	if found != 2 {
		t.Fatalf("expected both issues to include ship_hook, got %+v", items)
	}
}

func TestIssueServiceGet_SerializesFiredShipHook(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	issue := createTestIssue(t, svc.db, project.ID)

	now := time.Now().UTC()
	ok := true
	skipped := true
	if err := svc.db.Create(&model.IssueShipHook{
		IssueID:             issue.ID,
		ProjectID:           project.ID,
		Status:              model.IssueShipHookStatusFired,
		CommentEnabled:      true,
		CommentBody:         "已随 {version} 发出。",
		CommentRenderedBody: "已随 1.2.0 发出。",
		CloseEnabled:        true,
		WorkflowEnabled:     true,
		WorkflowStatus:      model.IssueWorkflowStatusDone,
		FiredVersionID:      uuid.NewString(),
		FiredVersionNumber:  "1.2.0",
		FiredReleaseURL:     "https://github.com/org/repo/releases/tag/1.2.0",
		FiredAt:             &now,
		CommentOK:           &ok,
		CloseOK:             &ok,
		CloseSkipped:        skipped,
		WorkflowOK:          &ok,
		CreatedByUserID:     user.ID,
		UpdatedByUserID:     user.ID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}).Error; err != nil {
		t.Fatalf("seed fired hook: %v", err)
	}

	got, err := svc.issueService.Get(issue.ID, user.ID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if got.ShipHook == nil {
		t.Fatalf("expected fired ship_hook")
	}
	data, err := json.Marshal(got.ShipHook)
	if err != nil {
		t.Fatalf("marshal ship_hook: %v", err)
	}
	payload := string(data)
	for _, want := range []string{
		`"status":"fired"`,
		`"comment_enabled":true`,
		`"close_enabled":true`,
		`"workflow_enabled":true`,
		`"workflow_status":"done"`,
		`"version_number":"1.2.0"`,
		`"comment_body":"已随 1.2.0 发出。"`,
		`"results"`,
		`"skipped":true`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("expected %q in %s", want, payload)
		}
	}
}
