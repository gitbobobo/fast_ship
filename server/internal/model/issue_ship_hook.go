package model

import "time"

type IssueShipHookStatus string

const (
	IssueShipHookStatusPending IssueShipHookStatus = "pending"
	// running means the hook has been claimed by a ship worker. It is kept
	// recoverable so a process crash cannot turn a pending hook into a lost one.
	IssueShipHookStatusRunning IssueShipHookStatus = "running"
	IssueShipHookStatusFired   IssueShipHookStatus = "fired"
)

type IssueShipHook struct {
	IssueID   string              `gorm:"type:text;primaryKey" json:"issue_id"`
	ProjectID string              `gorm:"type:text;not null;index" json:"project_id"`
	Status    IssueShipHookStatus `gorm:"type:text;not null;index" json:"status"`

	CommentEnabled bool   `gorm:"not null;default:false" json:"comment_enabled"`
	CommentBody    string `gorm:"type:text" json:"comment_body"`

	CloseEnabled bool `gorm:"not null;default:false" json:"close_enabled"`

	WorkflowEnabled bool                `gorm:"not null;default:false" json:"workflow_enabled"`
	WorkflowStatus  IssueWorkflowStatus `gorm:"type:text;not null;default:''" json:"workflow_status"`

	FiredVersionID     string     `gorm:"type:text" json:"fired_version_id"`
	FiredVersionNumber string     `gorm:"type:text" json:"fired_version_number"`
	FiredReleaseURL    string     `gorm:"type:text" json:"fired_release_url"`
	FiredAt            *time.Time `json:"fired_at"`
	ExecutionToken     string     `gorm:"type:text;index" json:"-"`
	LeaseToken         string     `gorm:"type:text;index" json:"-"`
	LeaseExpiresAt     *time.Time `json:"-"`
	RetryPending       bool       `gorm:"not null;default:false;index" json:"-"`

	CommentOK           *bool  `json:"comment_ok"`
	CommentSkipped      bool   `gorm:"not null;default:false" json:"comment_skipped"`
	CommentError        string `gorm:"type:text" json:"comment_error"`
	CommentRenderedBody string `gorm:"type:text" json:"comment_rendered_body"`

	CloseOK      *bool  `json:"close_ok"`
	CloseSkipped bool   `gorm:"not null;default:false" json:"close_skipped"`
	CloseError   string `gorm:"type:text" json:"close_error"`

	WorkflowOK      *bool  `json:"workflow_ok"`
	WorkflowSkipped bool   `gorm:"not null;default:false" json:"workflow_skipped"`
	WorkflowError   string `gorm:"type:text" json:"workflow_error"`

	CreatedByUserID string    `gorm:"type:text" json:"created_by_user_id"`
	UpdatedByUserID string    `gorm:"type:text" json:"updated_by_user_id"`
	CreatedAt       time.Time `gorm:"not null;index" json:"created_at"`
	UpdatedAt       time.Time `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueShipHook) TableName() string {
	return "issue_ship_hooks"
}
