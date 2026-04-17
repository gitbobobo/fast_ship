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

func (r *IssueRepository) Create(issue *model.Issue) error {
	return r.db.Create(issue).Error
}

func (r *IssueRepository) Save(issue *model.Issue) error {
	return r.db.Save(issue).Error
}

func (r *IssueRepository) FindByID(id string) (*model.Issue, error) {
	var issue model.Issue
	if err := r.db.Preload("GitHubMeta").Where("id = ?", id).First(&issue).Error; err != nil {
		return nil, err
	}
	return &issue, nil
}

func (r *IssueRepository) ListByProject(projectID string) ([]model.Issue, error) {
	var issues []model.Issue
	err := r.db.Preload("GitHubMeta").
		Where("project_id = ?", projectID).
		Order("updated_at DESC, sequence_number DESC").
		Find(&issues).Error
	return issues, err
}

func (r *IssueRepository) NextSequenceNumber(projectID string) (int, error) {
	var currentMax int
	if err := r.db.Model(&model.Issue{}).
		Where("project_id = ?", projectID).
		Select("COALESCE(MAX(sequence_number), 0)").
		Scan(&currentMax).Error; err != nil {
		return 0, err
	}
	return currentMax + 1, nil
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

type IssueGitHubMetaRepository struct {
	db *gorm.DB
}

func NewIssueGitHubMetaRepository(db *gorm.DB) *IssueGitHubMetaRepository {
	return &IssueGitHubMetaRepository{db: db}
}

func (r *IssueGitHubMetaRepository) Upsert(meta *model.IssueGitHubMeta) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}, {Name: "github_issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"issue_id",
			"github_node_id",
			"number",
			"html_url",
			"author_association",
			"assignees_json",
			"labels_json",
			"milestone_json",
			"reactions_json",
			"comments_count",
			"locked",
			"active_lock_reason",
			"synced_at",
			"raw_json",
		}),
	}).Create(meta).Error
}

func (r *IssueGitHubMetaRepository) FindByProjectAndGitHubID(projectID string, gitHubIssueID int64) (*model.IssueGitHubMeta, error) {
	var meta model.IssueGitHubMeta
	if err := r.db.Where("project_id = ? AND github_issue_id = ?", projectID, gitHubIssueID).First(&meta).Error; err != nil {
		return nil, err
	}
	return &meta, nil
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
			"source",
			"author_user_id",
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

func (r *IssueCommentRepository) Create(comment *model.IssueComment) error {
	return r.db.Create(comment).Error
}

func (r *IssueCommentRepository) DeleteMissing(issueID string, gitHubCommentIDs []int64) error {
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

func (r *IssueCommentRepository) ListAllByIssueID(issueID string) ([]model.IssueComment, error) {
	var comments []model.IssueComment
	err := r.db.
		Where("issue_id = ?", issueID).
		Order("created_at ASC, id ASC").
		Find(&comments).Error
	return comments, err
}

func (r *IssueCommentRepository) NextSyntheticCommentID(issueID string) (int64, error) {
	var currentMin *int64
	if err := r.db.Model(&model.IssueComment{}).
		Where("issue_id = ? AND github_comment_id < 0", issueID).
		Select("MIN(github_comment_id)").
		Scan(&currentMin).Error; err != nil {
		return 0, err
	}
	if currentMin == nil {
		return -1, nil
	}
	return *currentMin - 1, nil
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

type IssueInternalMetaRepository struct {
	db *gorm.DB
}

func NewIssueInternalMetaRepository(db *gorm.DB) *IssueInternalMetaRepository {
	return &IssueInternalMetaRepository{db: db}
}

func (r *IssueInternalMetaRepository) Get(issueID string) (*model.IssueInternalMeta, error) {
	var meta model.IssueInternalMeta
	if err := r.db.Where("issue_id = ?", issueID).First(&meta).Error; err != nil {
		return nil, err
	}
	return &meta, nil
}

func (r *IssueInternalMetaRepository) ListByIssueIDs(issueIDs []string) (map[string]model.IssueInternalMeta, error) {
	result := make(map[string]model.IssueInternalMeta, len(issueIDs))
	if len(issueIDs) == 0 {
		return result, nil
	}

	var metas []model.IssueInternalMeta
	if err := r.db.Where("issue_id IN ?", issueIDs).Find(&metas).Error; err != nil {
		return nil, err
	}

	for _, meta := range metas {
		result[meta.IssueID] = meta
	}
	return result, nil
}

func (r *IssueInternalMetaRepository) Upsert(meta *model.IssueInternalMeta) error {
	return r.UpsertTx(r.db, meta)
}

func (r *IssueInternalMetaRepository) UpsertTx(tx *gorm.DB, meta *model.IssueInternalMeta) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"workflow_status",
			"progress_percent",
			"checklist_total",
			"checklist_done",
			"started_at",
			"completed_at",
			"checklist_updated_at",
			"updated_by_user_id",
			"updated_at",
		}),
	}).Create(meta).Error
}

type IssueChecklistRepository struct {
	db *gorm.DB
}

func NewIssueChecklistRepository(db *gorm.DB) *IssueChecklistRepository {
	return &IssueChecklistRepository{db: db}
}

func (r *IssueChecklistRepository) ListByIssueID(issueID string) ([]model.IssueChecklistItem, error) {
	var items []model.IssueChecklistItem
	err := r.db.
		Where("issue_id = ?", issueID).
		Order("sort_order ASC, created_at ASC, id ASC").
		Find(&items).Error
	return items, err
}

func (r *IssueChecklistRepository) Transaction(fc func(tx *gorm.DB) error) error {
	return r.db.Transaction(fc)
}

func (r *IssueChecklistRepository) ReplaceForIssueTx(tx *gorm.DB, issueID string, items []model.IssueChecklistItem) error {
	if err := tx.Where("issue_id = ?", issueID).Delete(&model.IssueChecklistItem{}).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	return tx.Create(&items).Error
}
