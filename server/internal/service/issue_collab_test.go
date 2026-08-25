package service

import (
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

func setupCollabIssue(t *testing.T) (*testServices, *model.Issue, string) {
	t.Helper()
	ts := setupTestServices(t)
	ownerID := uuid.NewString()
	createTestUser(t, ts.db, ownerID)
	project := createTestProject(t, ts.db, ownerID)
	issue := createTestIssue(t, ts.db, project.ID)
	return ts, issue, ownerID
}

func TestIssueCollab_GetAreaEmpty(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	area, err := ts.collabService.GetArea(issue.ID, ownerID)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	if area.Consensus != nil || area.Summary != nil {
		t.Fatalf("expected empty area, got %+v", area)
	}
}

func TestIssueCollab_ConsensusUpsert(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	consensus1, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindConsensus, UpsertIssueCollabRequest{Body: "  共识初版  "})
	if err != nil {
		t.Fatalf("upsert consensus: %v", err)
	}
	if consensus1.Body != "共识初版" || consensus1.Author.Kind != string(model.CollabAuthorAgent) {
		t.Fatalf("unexpected consensus: %+v", consensus1)
	}

	consensus2, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindConsensus, UpsertIssueCollabRequest{Body: "共识更新"})
	if err != nil {
		t.Fatalf("upsert consensus again: %v", err)
	}
	if consensus2.Body != "共识更新" {
		t.Fatalf("expected updated body, got %q", consensus2.Body)
	}
	if consensus2.CreatedAt != consensus1.CreatedAt {
		t.Fatalf("expected created_at preserved, got %s vs %s", consensus2.CreatedAt, consensus1.CreatedAt)
	}

	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if area.Consensus == nil || area.Consensus.Body != "共识更新" {
		t.Fatalf("unexpected area consensus: %+v", area.Consensus)
	}
}

func TestIssueCollab_SummaryUpsert(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	s1, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindSummary, UpsertIssueCollabRequest{Body: "  已完成  "})
	if err != nil {
		t.Fatalf("upsert summary: %v", err)
	}
	if s1.Body != "已完成" {
		t.Fatalf("unexpected summary body: %q", s1.Body)
	}

	s2, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindSummary, UpsertIssueCollabRequest{Body: "已更新"})
	if err != nil {
		t.Fatalf("upsert summary again: %v", err)
	}
	if s2.Body != "已更新" || s2.CreatedAt != s1.CreatedAt {
		t.Fatalf("unexpected upsert result: %+v", s2)
	}

	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if area.Summary == nil || area.Summary.Body != "已更新" {
		t.Fatalf("unexpected area summary: %+v", area.Summary)
	}
}

func TestIssueCollab_InvalidKind(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	if _, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKind("invalid"), UpsertIssueCollabRequest{Body: "x"}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for invalid kind upsert, got %v", err)
	}
	if err := ts.collabService.Delete(issue.ID, ownerID, model.CollabDocumentKind("invalid")); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for invalid kind delete, got %v", err)
	}
}

func TestIssueCollab_Validation(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	if _, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindConsensus, UpsertIssueCollabRequest{Body: "  "}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty consensus body, got %v", err)
	}
	if _, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindSummary, UpsertIssueCollabRequest{Body: " "}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty summary body, got %v", err)
	}
}

func TestIssueCollab_AgentAuthoredGetArea(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	if _, err := ts.collabService.Upsert(issue.ID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindConsensus, UpsertIssueCollabRequest{Body: "代理共识"}); err != nil {
		t.Fatalf("upsert consensus: %v", err)
	}

	area, err := ts.collabService.GetArea(issue.ID, ownerID)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	if area.Consensus == nil || area.Consensus.Author.Kind != string(model.CollabAuthorAgent) || area.Consensus.Author.Login != collabAgentLogin {
		t.Fatalf("unexpected consensus actor: %+v", area.Consensus)
	}
}

func TestIssueCollab_AccessControl(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)
	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)

	if _, err := ts.collabService.GetArea(issue.ID, otherID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner, got %v", err)
	}

	if _, err := ts.collabService.GetArea(uuid.NewString(), ownerID); err != errs.ErrIssueNotFound {
		t.Fatalf("expected ErrIssueNotFound for missing issue, got %v", err)
	}

	if _, err := ts.collabService.Upsert(issue.ID, otherID, model.CollabAuthorAgent, model.CollabDocumentKindConsensus, UpsertIssueCollabRequest{Body: "x"}); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner write, got %v", err)
	}
}

func seedFullCollabArea(t *testing.T, ts *testServices, issueID, ownerID string) {
	t.Helper()
	if _, err := ts.collabService.Upsert(issueID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindConsensus, UpsertIssueCollabRequest{Body: "共识"}); err != nil {
		t.Fatalf("seed consensus: %v", err)
	}
	if _, err := ts.collabService.Upsert(issueID, ownerID, model.CollabAuthorAgent, model.CollabDocumentKindSummary, UpsertIssueCollabRequest{Body: "总结"}); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
}

func TestIssueCollab_ClearArea(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)
	seedFullCollabArea(t, ts, issue.ID, ownerID)

	if err := ts.collabService.ClearArea(issue.ID, ownerID); err != nil {
		t.Fatalf("clear area: %v", err)
	}
	area, err := ts.collabService.GetArea(issue.ID, ownerID)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	if area.Consensus != nil || area.Summary != nil {
		t.Fatalf("expected empty area after clear, got %+v", area)
	}
}

func TestIssueCollab_DeleteSections(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)
	seedFullCollabArea(t, ts, issue.ID, ownerID)

	if err := ts.collabService.Delete(issue.ID, ownerID, model.CollabDocumentKindConsensus); err != nil {
		t.Fatalf("delete consensus: %v", err)
	}
	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if area.Consensus != nil || area.Summary == nil {
		t.Fatalf("expected consensus removed only, got %+v", area)
	}
}

func TestIssueCollab_DeleteIdempotent(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	if err := ts.collabService.Delete(issue.ID, ownerID, model.CollabDocumentKindConsensus); err != nil {
		t.Fatalf("delete missing consensus: %v", err)
	}
	if err := ts.collabService.ClearArea(issue.ID, ownerID); err != nil {
		t.Fatalf("clear empty area: %v", err)
	}
	if err := ts.collabService.Delete(issue.ID, ownerID, model.CollabDocumentKindSummary); err != nil {
		t.Fatalf("delete missing summary: %v", err)
	}
}

func TestIssueCollab_DeleteAccessControl(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)
	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)

	if err := ts.collabService.Delete(issue.ID, otherID, model.CollabDocumentKindConsensus); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner delete consensus, got %v", err)
	}
	if err := ts.collabService.Delete(issue.ID, otherID, model.CollabDocumentKindSummary); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner delete summary, got %v", err)
	}
	if err := ts.collabService.ClearArea(issue.ID, otherID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner clear area, got %v", err)
	}
	if err := ts.collabService.ClearArea(uuid.NewString(), ownerID); err != errs.ErrIssueNotFound {
		t.Fatalf("expected ErrIssueNotFound for missing issue delete, got %v", err)
	}
}
