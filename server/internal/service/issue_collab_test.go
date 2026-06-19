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
	if len(area.Notes) != 0 || len(area.Questions) != 0 || area.Summary != nil {
		t.Fatalf("expected empty area, got %+v", area)
	}
}

func TestIssueCollab_NoteCRUD(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	note, err := ts.collabService.CreateNote(issue.ID, ownerID, model.CollabAuthorUser, CreateIssueCollabNoteRequest{Body: "  hello  "})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	if note.Body != "hello" {
		t.Fatalf("expected trimmed body, got %q", note.Body)
	}
	if note.Author.Kind != string(model.CollabAuthorUser) || note.Author.Login != "user_"+ownerID {
		t.Fatalf("unexpected actor: %+v", note.Author)
	}

	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if len(area.Notes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(area.Notes))
	}

	updated, err := ts.collabService.UpdateNote(issue.ID, note.ID, ownerID, UpdateIssueCollabNoteRequest{Body: "world"})
	if err != nil {
		t.Fatalf("update note: %v", err)
	}
	if updated.Body != "world" {
		t.Fatalf("expected updated body, got %q", updated.Body)
	}

	if err := ts.collabService.DeleteNote(issue.ID, note.ID, ownerID); err != nil {
		t.Fatalf("delete note: %v", err)
	}
	area, _ = ts.collabService.GetArea(issue.ID, ownerID)
	if len(area.Notes) != 0 {
		t.Fatalf("expected 0 notes after delete, got %d", len(area.Notes))
	}
}

func TestIssueCollab_QuestionsAndAnswer(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	questions, err := ts.collabService.CreateQuestions(issue.ID, ownerID, model.CollabAuthorAgent, CreateIssueCollabQuestionsRequest{
		Items: []IssueCollabQuestionInput{
			{Body: "按钮放哪里？", Options: []string{"顶部", "侧边"}},
			{Body: "补充说明？"},
		},
	})
	if err != nil {
		t.Fatalf("create questions: %v", err)
	}
	if len(questions) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(questions))
	}
	if questions[0].SortOrder != 0 || questions[1].SortOrder != 1 {
		t.Fatalf("unexpected sort order: %d %d", questions[0].SortOrder, questions[1].SortOrder)
	}
	if questions[0].Author.Kind != string(model.CollabAuthorAgent) || questions[0].Author.Login != collabAgentLogin {
		t.Fatalf("expected agent actor, got %+v", questions[0].Author)
	}
	if questions[0].Answer != nil {
		t.Fatal("expected nil answer before answering")
	}
	if len(questions[0].Options) != 2 || len(questions[1].Options) != 0 {
		t.Fatalf("unexpected options: %+v %+v", questions[0].Options, questions[1].Options)
	}

	// 作答
	answered, err := ts.collabService.AnswerQuestion(issue.ID, questions[0].ID, ownerID, model.CollabAuthorUser, AnswerIssueCollabQuestionRequest{Answer: "顶部"})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if answered.Answer == nil || answered.Answer.Value != "顶部" {
		t.Fatalf("unexpected answer: %+v", answered.Answer)
	}
	if answered.Answer.Author.Kind != string(model.CollabAuthorUser) {
		t.Fatalf("expected user answer actor: %+v", answered.Answer.Author)
	}

	// 改答覆盖
	reAnswered, err := ts.collabService.AnswerQuestion(issue.ID, questions[0].ID, ownerID, model.CollabAuthorUser, AnswerIssueCollabQuestionRequest{Answer: "侧边"})
	if err != nil {
		t.Fatalf("re-answer: %v", err)
	}
	if reAnswered.Answer.Value != "侧边" {
		t.Fatalf("expected overridden answer, got %q", reAnswered.Answer.Value)
	}

	// 第二批问题 sort_order 继续
	next, err := ts.collabService.CreateQuestions(issue.ID, ownerID, model.CollabAuthorAgent, CreateIssueCollabQuestionsRequest{
		Items: []IssueCollabQuestionInput{{Body: "Q3"}},
	})
	if err != nil {
		t.Fatalf("create next: %v", err)
	}
	if next[0].SortOrder != 2 {
		t.Fatalf("expected sort_order 2, got %d", next[0].SortOrder)
	}

	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if len(area.Questions) != 3 {
		t.Fatalf("expected 3 questions in area, got %d", len(area.Questions))
	}
	if area.Questions[0].Answer == nil || area.Questions[0].Answer.Value != "侧边" {
		t.Fatalf("expected answered question in area: %+v", area.Questions[0].Answer)
	}
}

func TestIssueCollab_SummaryUpsert(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	s1, err := ts.collabService.UpsertSummary(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabSummaryRequest{
		Body:      "已完成",
		CommitIDs: []string{"abc1234", "0123456789abcdef0123456789abcdef01234567"},
	})
	if err != nil {
		t.Fatalf("upsert summary: %v", err)
	}
	if len(s1.CommitIDs) != 2 {
		t.Fatalf("expected 2 commit ids, got %d", len(s1.CommitIDs))
	}

	// 覆盖更新
	s2, err := ts.collabService.UpsertSummary(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabSummaryRequest{
		Body:      "已更新",
		CommitIDs: []string{"abcdef1"},
	})
	if err != nil {
		t.Fatalf("upsert summary again: %v", err)
	}
	if s2.Body != "已更新" || len(s2.CommitIDs) != 1 {
		t.Fatalf("unexpected upsert result: %+v", s2)
	}

	// 仍只有一条
	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if area.Summary == nil || area.Summary.Body != "已更新" {
		t.Fatalf("unexpected area summary: %+v", area.Summary)
	}
}

func TestIssueCollab_Validation(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	if _, err := ts.collabService.CreateNote(issue.ID, ownerID, model.CollabAuthorUser, CreateIssueCollabNoteRequest{Body: "   "}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty note, got %v", err)
	}

	tooManyOptions := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if _, err := ts.collabService.CreateQuestions(issue.ID, ownerID, model.CollabAuthorAgent, CreateIssueCollabQuestionsRequest{
		Items: []IssueCollabQuestionInput{{Body: "q", Options: tooManyOptions}},
	}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for too many options, got %v", err)
	}

	if _, err := ts.collabService.CreateQuestions(issue.ID, ownerID, model.CollabAuthorAgent, CreateIssueCollabQuestionsRequest{Items: nil}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty items, got %v", err)
	}

	tooManyItems := make([]IssueCollabQuestionInput, collabMaxQuestionsPerBatch+1)
	if _, err := ts.collabService.CreateQuestions(issue.ID, ownerID, model.CollabAuthorAgent, CreateIssueCollabQuestionsRequest{Items: tooManyItems}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for too many questions, got %v", err)
	}

	if _, err := ts.collabService.UpsertSummary(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabSummaryRequest{
		Body: "s", CommitIDs: []string{"not-a-sha"},
	}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for bad commit id, got %v", err)
	}
}

func TestIssueCollab_AccessControl(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)
	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)

	// 非所有者无权访问
	if _, err := ts.collabService.GetArea(issue.ID, otherID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner, got %v", err)
	}

	// 问题不存在
	if _, err := ts.collabService.GetArea(uuid.NewString(), ownerID); err != errs.ErrIssueNotFound {
		t.Fatalf("expected ErrIssueNotFound for missing issue, got %v", err)
	}

	// note 不属于该 issue
	if _, err := ts.collabService.UpdateNote(issue.ID, uuid.NewString(), ownerID, UpdateIssueCollabNoteRequest{Body: "x"}); err != errs.ErrIssueCollabNotFound {
		t.Fatalf("expected ErrIssueCollabNotFound for mismatched note, got %v", err)
	}

	// question 不存在
	if _, err := ts.collabService.AnswerQuestion(issue.ID, uuid.NewString(), ownerID, model.CollabAuthorUser, AnswerIssueCollabQuestionRequest{Answer: "a"}); err != errs.ErrIssueCollabNotFound {
		t.Fatalf("expected ErrIssueCollabNotFound for missing question, got %v", err)
	}
}
