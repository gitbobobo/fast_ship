package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueRepository struct {
	db *gorm.DB
}

func NewIssueRepository(db *gorm.DB) *IssueRepository {
	return &IssueRepository{db: db}
}

func (r *IssueRepository) Upsert(issue *model.Issue) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}, {Name: "github_issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"github_node_id",
			"number",
			"state",
			"state_reason",
			"title",
			"body",
			"body_html",
			"html_url",
			"author_login",
			"author_avatar_url",
			"author_association",
			"assignees_json",
			"labels_json",
			"milestone_json",
			"reactions_json",
			"comments_count",
			"locked",
			"active_lock_reason",
			"closed_at",
			"created_at",
			"updated_at",
			"synced_at",
			"raw_json",
		}),
	}).Create(issue).Error
}

func (r *IssueRepository) FindByProjectAndGitHubID(projectID string, gitHubIssueID int64) (*model.Issue, error) {
	var issue model.Issue
	if err := r.db.Where("project_id = ? AND github_issue_id = ?", projectID, gitHubIssueID).First(&issue).Error; err != nil {
		return nil, err
	}
	return &issue, nil
}

func (r *IssueRepository) FindByID(id string) (*model.Issue, error) {
	var issue model.Issue
	if err := r.db.Where("id = ?", id).First(&issue).Error; err != nil {
		return nil, err
	}
	return &issue, nil
}

func (r *IssueRepository) ListByProject(projectID string) ([]model.Issue, error) {
	var issues []model.Issue
	err := r.db.Where("project_id = ?", projectID).
		Order("updated_at DESC, number DESC").
		Find(&issues).Error
	return issues, err
}

func (r *IssueRepository) List(projectID, state, query string, page, pageSize int) ([]model.Issue, int64, error) {
	var issues []model.Issue
	var total int64

	dbq := r.db.Model(&model.Issue{}).Where("project_id = ?", projectID)
	if state != "" {
		dbq = dbq.Where("state = ?", state)
	}
	if query != "" {
		like := "%" + query + "%"
		dbq = dbq.Where(
			r.db.Where("title LIKE ?", like).
				Or("body LIKE ?", like).
				Or("author_login LIKE ?", like).
				Or("CAST(number AS TEXT) = ?", query),
		)
	}

	if err := dbq.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := dbq.Order("updated_at DESC, number DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&issues).Error
	return issues, total, err
}

func (r *IssueRepository) ListStaleProjectIDs(before time.Time, limit int) ([]string, error) {
	var ids []string
	query := r.db.Model(&model.IssueSyncState{}).
		Where("status != ?", model.IssueSyncStatusRunning).
		Where("last_synced_at IS NULL OR last_synced_at < ?", before).
		Order("last_synced_at ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Pluck("project_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

type IssueCommentRepository struct {
	db *gorm.DB
}

func NewIssueCommentRepository(db *gorm.DB) *IssueCommentRepository {
	return &IssueCommentRepository{db: db}
}

func (r *IssueCommentRepository) Upsert(comment *model.IssueComment) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}, {Name: "github_comment_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"github_node_id",
			"body",
			"body_html",
			"html_url",
			"author_login",
			"author_avatar_url",
			"author_association",
			"reactions_json",
			"created_at",
			"updated_at",
			"raw_json",
		}),
	}).Create(comment).Error
}

func (r *IssueCommentRepository) DeleteMissing(issueID string, gitHubCommentIDs []int64) error {
	// Keep the latest snapshot of comments for each issue to reflect deletions on GitHub.
	query := r.db.Where("issue_id = ?", issueID)
	if len(gitHubCommentIDs) > 0 {
		query = query.Where("github_comment_id NOT IN ?", gitHubCommentIDs)
	}
	return query.Delete(&model.IssueComment{}).Error
}

func (r *IssueCommentRepository) List(issueID string, page, pageSize int) ([]model.IssueComment, int64, error) {
	var comments []model.IssueComment
	var total int64

	query := r.db.Model(&model.IssueComment{}).Where("issue_id = ?", issueID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&comments).Error
	return comments, total, err
}

type IssueTimelineRepository struct {
	db *gorm.DB
}

func NewIssueTimelineRepository(db *gorm.DB) *IssueTimelineRepository {
	return &IssueTimelineRepository{db: db}
}

func (r *IssueTimelineRepository) Upsert(event *model.IssueTimelineEvent) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}, {Name: "event_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"github_event_id",
			"event_type",
			"actor_login",
			"actor_avatar_url",
			"body",
			"summary",
			"payload_json",
			"created_at",
		}),
	}).Create(event).Error
}

func (r *IssueTimelineRepository) DeleteMissing(issueID string, eventKeys []string) error {
	query := r.db.Where("issue_id = ?", issueID)
	if len(eventKeys) > 0 {
		query = query.Where("event_key NOT IN ?", eventKeys)
	}
	return query.Delete(&model.IssueTimelineEvent{}).Error
}

func (r *IssueTimelineRepository) List(issueID string, includeCommented bool, page, pageSize int) ([]model.IssueTimelineEvent, int64, error) {
	var events []model.IssueTimelineEvent
	var total int64

	query := r.db.Model(&model.IssueTimelineEvent{}).Where("issue_id = ?", issueID)
	if !includeCommented {
		query = query.Where("event_type != ?", "commented")
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&events).Error
	return events, total, err
}

type IssueSyncStateRepository struct {
	db *gorm.DB
}

func NewIssueSyncStateRepository(db *gorm.DB) *IssueSyncStateRepository {
	return &IssueSyncStateRepository{db: db}
}

func (r *IssueSyncStateRepository) Get(projectID string) (*model.IssueSyncState, error) {
	var state model.IssueSyncState
	if err := r.db.Where("project_id = ?", projectID).First(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *IssueSyncStateRepository) GetOrCreate(projectID string) (*model.IssueSyncState, error) {
	state := &model.IssueSyncState{
		ProjectID: projectID,
		Status:    model.IssueSyncStatusIdle,
	}
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}},
		DoNothing: true,
	}).Create(state).Error; err != nil {
		return nil, err
	}
	return r.Get(projectID)
}

func (r *IssueSyncStateRepository) Save(state *model.IssueSyncState) error {
	return r.db.Save(state).Error
}
