package model

import "time"

type CollabAuthorKind string

const (
	CollabAuthorUser  CollabAuthorKind = "user"
	CollabAuthorAgent CollabAuthorKind = "agent"
)

func (k CollabAuthorKind) Valid() bool {
	return k == CollabAuthorUser || k == CollabAuthorAgent
}

type IssueCollabSuggestion struct {
	ID           string           `gorm:"type:text;primaryKey" json:"id"`
	IssueID      string           `gorm:"type:text;not null;index:idx_collab_suggestions_issue_sort,priority:1" json:"issue_id"`
	Body         string           `gorm:"type:text;not null" json:"body"`
	SortOrder    int              `gorm:"not null;default:0;index:idx_collab_suggestions_issue_sort,priority:2" json:"sort_order"`
	AuthorUserID string           `gorm:"type:text;not null" json:"author_user_id"`
	AuthorKind   CollabAuthorKind `gorm:"type:text;not null;default:agent" json:"author_kind"`
	CreatedAt    time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time        `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueCollabSuggestion) TableName() string {
	return "issue_collab_suggestions"
}

type IssueCollabPlan struct {
	IssueID      string           `gorm:"type:text;primaryKey" json:"issue_id"`
	Body         string           `gorm:"type:text;not null" json:"body"`
	AuthorUserID string           `gorm:"type:text;not null" json:"author_user_id"`
	AuthorKind   CollabAuthorKind `gorm:"type:text;not null;default:agent" json:"author_kind"`
	CreatedAt    time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time        `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueCollabPlan) TableName() string {
	return "issue_collab_plans"
}

type IssueCollabReview struct {
	IssueID      string           `gorm:"type:text;primaryKey" json:"issue_id"`
	Body         string           `gorm:"type:text;not null" json:"body"`
	AuthorUserID string           `gorm:"type:text;not null" json:"author_user_id"`
	AuthorKind   CollabAuthorKind `gorm:"type:text;not null;default:agent" json:"author_kind"`
	CreatedAt    time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time        `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueCollabReview) TableName() string {
	return "issue_collab_reviews"
}

type IssueCollabSummary struct {
	IssueID       string           `gorm:"type:text;primaryKey" json:"issue_id"`
	Body          string           `gorm:"type:text;not null" json:"body"`
	CommitIDsJSON string           `gorm:"type:text" json:"-"`
	AuthorUserID  string           `gorm:"type:text;not null" json:"author_user_id"`
	AuthorKind    CollabAuthorKind `gorm:"type:text;not null;default:agent" json:"author_kind"`
	CreatedAt     time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt     time.Time        `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueCollabSummary) TableName() string {
	return "issue_collab_summaries"
}
