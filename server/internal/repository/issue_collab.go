package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueCollabRepository struct {
	db *gorm.DB
}

func NewIssueCollabRepository(db *gorm.DB) *IssueCollabRepository {
	return &IssueCollabRepository{db: db}
}

func (r *IssueCollabRepository) Transaction(fc func(tx *gorm.DB) error) error {
	return r.db.Transaction(fc)
}

func (r *IssueCollabRepository) ListNotesByIssueID(issueID string) ([]model.IssueCollabNote, error) {
	var notes []model.IssueCollabNote
	err := r.db.
		Where("issue_id = ?", issueID).
		Order("created_at ASC, id ASC").
		Find(&notes).Error
	return notes, err
}

func (r *IssueCollabRepository) GetNote(id string) (*model.IssueCollabNote, error) {
	var note model.IssueCollabNote
	if err := r.db.Where("id = ?", id).First(&note).Error; err != nil {
		return nil, err
	}
	return &note, nil
}

func (r *IssueCollabRepository) CreateNote(note *model.IssueCollabNote) error {
	return r.db.Create(note).Error
}

func (r *IssueCollabRepository) UpdateNote(id string, body string) error {
	return r.db.Model(&model.IssueCollabNote{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"body":       body,
			"updated_at": time.Now().UTC(),
		}).Error
}

func (r *IssueCollabRepository) DeleteNote(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.IssueCollabNote{}).Error
}

func (r *IssueCollabRepository) ListQuestionsByIssueID(issueID string) ([]model.IssueCollabQuestion, error) {
	var questions []model.IssueCollabQuestion
	err := r.db.
		Where("issue_id = ?", issueID).
		Order("sort_order ASC, created_at ASC, id ASC").
		Find(&questions).Error
	return questions, err
}

func (r *IssueCollabRepository) GetQuestion(id string) (*model.IssueCollabQuestion, error) {
	var question model.IssueCollabQuestion
	if err := r.db.Where("id = ?", id).First(&question).Error; err != nil {
		return nil, err
	}
	return &question, nil
}

func (r *IssueCollabRepository) CreateQuestionsTx(tx *gorm.DB, issueID string, questions []model.IssueCollabQuestion) error {
	if len(questions) == 0 {
		return nil
	}
	var maxSort int
	if err := tx.Model(&model.IssueCollabQuestion{}).
		Where("issue_id = ?", issueID).
		Select("COALESCE(MAX(sort_order), -1)").
		Scan(&maxSort).Error; err != nil {
		return err
	}
	for i := range questions {
		maxSort++
		questions[i].SortOrder = maxSort
	}
	return tx.Create(&questions).Error
}

func (r *IssueCollabRepository) UpdateAnswer(id string, value string, authorUserID string, authorKind model.CollabAuthorKind, answeredAt time.Time) error {
	return r.db.Model(&model.IssueCollabQuestion{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"answer_value":          value,
			"answer_author_user_id": authorUserID,
			"answer_author_kind":    authorKind,
			"answered_at":           answeredAt,
			"updated_at":            time.Now().UTC(),
		}).Error
}

func (r *IssueCollabRepository) DeleteQuestion(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.IssueCollabQuestion{}).Error
}

func (r *IssueCollabRepository) GetSummary(issueID string) (*model.IssueCollabSummary, error) {
	var summary model.IssueCollabSummary
	if err := r.db.Where("issue_id = ?", issueID).First(&summary).Error; err != nil {
		return nil, err
	}
	return &summary, nil
}

func (r *IssueCollabRepository) UpsertSummary(summary *model.IssueCollabSummary) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"body",
			"commit_ids_json",
			"author_user_id",
			"author_kind",
			"updated_at",
		}),
	}).Create(summary).Error
}
