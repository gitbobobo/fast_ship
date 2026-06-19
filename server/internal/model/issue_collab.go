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

type IssueCollabNote struct {
	ID           string           `gorm:"type:text;primaryKey" json:"id"`
	IssueID      string           `gorm:"type:text;not null;index" json:"issue_id"`
	Body         string           `gorm:"type:text;not null" json:"body"`
	AuthorUserID string           `gorm:"type:text;not null" json:"author_user_id"`
	AuthorKind   CollabAuthorKind `gorm:"type:text;not null;default:user" json:"author_kind"`
	CreatedAt    time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time        `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueCollabNote) TableName() string {
	return "issue_collab_notes"
}

type IssueCollabQuestion struct {
	ID                 string           `gorm:"type:text;primaryKey" json:"id"`
	IssueID            string           `gorm:"type:text;not null;index" json:"issue_id"`
	Body               string           `gorm:"type:text;not null" json:"body"`
	OptionsJSON        string           `gorm:"type:text" json:"-"`
	SortOrder          int              `gorm:"not null;default:0;index" json:"sort_order"`
	AuthorUserID       string           `gorm:"type:text;not null" json:"author_user_id"`
	AuthorKind         CollabAuthorKind `gorm:"type:text;not null;default:agent" json:"author_kind"`
	AnswerValue        string           `gorm:"type:text" json:"answer_value"`
	AnswerAuthorUserID string           `gorm:"type:text" json:"answer_author_user_id"`
	AnswerAuthorKind   CollabAuthorKind `gorm:"type:text" json:"answer_author_kind"`
	AnsweredAt         *time.Time       `json:"answered_at"`
	CreatedAt          time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time        `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueCollabQuestion) TableName() string {
	return "issue_collab_questions"
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
