package model

import "time"

type IssueSource string

const (
	IssueSourceGitHub   IssueSource = "github"
	IssueSourceInternal IssueSource = "internal"
)

type IssueState string

const (
	IssueStateOpen   IssueState = "open"
	IssueStateClosed IssueState = "closed"
)

type IssueSyncStatus string

const (
	IssueSyncStatusIdle      IssueSyncStatus = "idle"
	IssueSyncStatusRunning   IssueSyncStatus = "running"
	IssueSyncStatusFailed    IssueSyncStatus = "failed"
	IssueSyncStatusCompleted IssueSyncStatus = "completed"
)

type IssueWorkflowStatus string

const (
	IssueWorkflowStatusTodo       IssueWorkflowStatus = "todo"
	IssueWorkflowStatusInProgress IssueWorkflowStatus = "in_progress"
	IssueWorkflowStatusDone       IssueWorkflowStatus = "done"
)

func IsValidIssueWorkflowStatus(status IssueWorkflowStatus) bool {
	switch status {
	case "", IssueWorkflowStatusTodo, IssueWorkflowStatusInProgress, IssueWorkflowStatusDone:
		return true
	default:
		return false
	}
}

type Issue struct {
	ID              string      `gorm:"type:text;primaryKey" json:"id"`
	ProjectID       string      `gorm:"type:text;not null;index" json:"project_id"`
	Source          IssueSource `gorm:"type:text;not null;default:internal;index" json:"source"`
	SequenceNumber  int         `gorm:"not null;default:0;index" json:"sequence_number"`
	State           IssueState  `gorm:"type:text;not null;index" json:"state"`
	StateReason     string      `gorm:"type:text" json:"state_reason"`
	Title           string      `gorm:"type:text;not null" json:"title"`
	Body            string      `gorm:"type:text" json:"body"`
	BodyHTML        string      `gorm:"column:body_html;type:text" json:"body_html"`
	AuthorUserID    string      `gorm:"type:text;index" json:"author_user_id"`
	AuthorLogin     string      `gorm:"type:text" json:"author_login"`
	AuthorAvatarURL string      `gorm:"type:text" json:"author_avatar_url"`
	ClosedAt        *time.Time  `json:"closed_at"`
	CreatedAt       time.Time   `gorm:"not null;index" json:"created_at"`
	UpdatedAt       time.Time   `gorm:"not null;index" json:"updated_at"`

	Project        Project              `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
	GitHubMeta     *IssueGitHubMeta     `gorm:"foreignKey:IssueID" json:"-"`
	Comments       []IssueComment       `gorm:"foreignKey:IssueID" json:"-"`
	TimelineEvents []IssueTimelineEvent `gorm:"foreignKey:IssueID" json:"-"`
}

func (Issue) TableName() string {
	return "issues"
}

type IssueGitHubMeta struct {
	IssueID           string    `gorm:"type:text;primaryKey" json:"issue_id"`
	ProjectID         string    `gorm:"type:text;not null;index" json:"project_id"`
	GitHubIssueID     int64     `gorm:"column:github_issue_id;not null;index" json:"github_issue_id"`
	GitHubNodeID      string    `gorm:"column:github_node_id;type:text" json:"github_node_id"`
	Number            int       `gorm:"not null;index" json:"number"`
	HTMLURL           string    `gorm:"type:text" json:"html_url"`
	AuthorAssociation string    `gorm:"type:text" json:"author_association"`
	AssigneesJSON     string    `gorm:"type:text" json:"-"`
	LabelsJSON        string    `gorm:"type:text" json:"-"`
	MilestoneJSON     string    `gorm:"type:text" json:"-"`
	ReactionsJSON     string    `gorm:"type:text" json:"-"`
	CommentsCount     int       `gorm:"not null;default:0" json:"comments_count"`
	Locked            bool      `gorm:"not null;default:false" json:"locked"`
	ActiveLockReason  string    `gorm:"type:text" json:"active_lock_reason"`
	SyncedAt          time.Time `gorm:"not null;index" json:"synced_at"`
	RawJSON           string    `gorm:"type:text" json:"-"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueGitHubMeta) TableName() string {
	return "issue_github_meta"
}

type IssueComment struct {
	ID                string      `gorm:"type:text;primaryKey" json:"id"`
	IssueID           string      `gorm:"type:text;not null;index" json:"issue_id"`
	Source            IssueSource `gorm:"type:text;not null;default:github;index" json:"source"`
	AuthorUserID      string      `gorm:"type:text;index" json:"author_user_id"`
	GitHubCommentID   int64       `gorm:"column:github_comment_id;not null;index" json:"github_comment_id"`
	GitHubNodeID      string      `gorm:"column:github_node_id;type:text" json:"github_node_id"`
	Body              string      `gorm:"type:text" json:"body"`
	BodyHTML          string      `gorm:"column:body_html;type:text" json:"body_html"`
	HTMLURL           string      `gorm:"type:text" json:"html_url"`
	AuthorLogin       string      `gorm:"type:text" json:"author_login"`
	AuthorAvatarURL   string      `gorm:"type:text" json:"author_avatar_url"`
	AuthorAssociation string      `gorm:"type:text" json:"author_association"`
	ReactionsJSON     string      `gorm:"type:text" json:"-"`
	GitHubCreatedAt   time.Time   `gorm:"column:created_at;not null;index" json:"created_at"`
	GitHubUpdatedAt   time.Time   `gorm:"column:updated_at;not null;index" json:"updated_at"`
	RawJSON           string      `gorm:"type:text" json:"-"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueComment) TableName() string {
	return "issue_comments"
}

type IssueTimelineEvent struct {
	ID              string    `gorm:"type:text;primaryKey" json:"id"`
	IssueID         string    `gorm:"type:text;not null;index" json:"issue_id"`
	EventKey        string    `gorm:"type:text;not null;index" json:"event_key"`
	GitHubEventID   int64     `gorm:"column:github_event_id;not null;default:0;index" json:"github_event_id"`
	EventType       string    `gorm:"type:text;not null;index" json:"event_type"`
	ActorLogin      string    `gorm:"type:text" json:"actor_login"`
	ActorAvatarURL  string    `gorm:"type:text" json:"actor_avatar_url"`
	Body            string    `gorm:"type:text" json:"body"`
	Summary         string    `gorm:"type:text" json:"summary"`
	PayloadJSON     string    `gorm:"type:text" json:"-"`
	GitHubCreatedAt time.Time `gorm:"column:created_at;not null;index" json:"created_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueTimelineEvent) TableName() string {
	return "issue_timeline_events"
}

type IssueSyncState struct {
	ProjectID            string          `gorm:"type:text;primaryKey" json:"project_id"`
	Status               IssueSyncStatus `gorm:"type:text;not null;default:idle" json:"status"`
	LastIssueUpdatedAt   *time.Time      `json:"last_issue_updated_at"`
	LastSyncedAt         *time.Time      `json:"last_synced_at"`
	LastSuccessfulSyncAt *time.Time      `json:"last_successful_sync_at"`
	LastError            string          `gorm:"type:text" json:"last_error"`
}

func (IssueSyncState) TableName() string {
	return "issue_sync_states"
}

type IssueInternalMeta struct {
	IssueID            string              `gorm:"type:text;primaryKey" json:"issue_id"`
	WorkflowStatus     IssueWorkflowStatus `gorm:"type:text;not null;default:''" json:"workflow_status"`
	ProgressPercent    *int                `gorm:"type:integer" json:"progress_percent"`
	ChecklistTotal     int                 `gorm:"not null;default:0" json:"checklist_total"`
	ChecklistDone      int                 `gorm:"not null;default:0" json:"checklist_done"`
	StartedAt          *time.Time          `json:"started_at"`
	CompletedAt        *time.Time          `json:"completed_at"`
	ChecklistUpdatedAt *time.Time          `json:"checklist_updated_at"`
	UpdatedByUserID    string              `gorm:"type:text" json:"updated_by_user_id"`
	CreatedAt          time.Time           `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time           `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueInternalMeta) TableName() string {
	return "issue_internal_meta"
}

type IssueChecklistItem struct {
	ID              string    `gorm:"type:text;primaryKey" json:"id"`
	IssueID         string    `gorm:"type:text;not null;index" json:"issue_id"`
	Title           string    `gorm:"type:text;not null" json:"title"`
	IsCompleted     bool      `gorm:"not null;default:false" json:"is_completed"`
	SortOrder       int       `gorm:"not null;default:0;index" json:"sort_order"`
	CreatedByUserID string    `gorm:"type:text" json:"created_by_user_id"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"not null" json:"updated_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueChecklistItem) TableName() string {
	return "issue_checklist_items"
}
