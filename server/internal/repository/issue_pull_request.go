package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssuePullRequestRepository struct {
	db *gorm.DB
}

func NewIssuePullRequestRepository(db *gorm.DB) *IssuePullRequestRepository {
	return &IssuePullRequestRepository{db: db}
}

func (r *IssuePullRequestRepository) Create(link *model.IssuePullRequest) error {
	return r.db.Create(link).Error
}

// Upsert 以业务唯一键 (issue_id, provider, repo_full_name, number) 原子插入或刷新：
// 冲突时更新同步字段并保留既有主键；link_origin 只升不降——手动 attach 可把
// synced 行升级为 manual，反向不允许（避免同步投影把手动关联降级、继而被
// DeleteMissingSynced 清掉）。
func (r *IssuePullRequestRepository) Upsert(link *model.IssuePullRequest) error {
	set := clause.AssignmentColumns([]string{
		"html_url", "title", "state", "is_draft", "author_login",
		"head_ref", "base_ref", "merged_at", "closed_at", "synced_at", "updated_at",
	})
	set = append(set, clause.Assignment{
		Column: clause.Column{Name: "link_origin"},
		Value:  gorm.Expr("CASE WHEN excluded.link_origin = 'manual' THEN 'manual' ELSE issue_pull_requests.link_origin END"),
	})
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "issue_id"},
			{Name: "provider"},
			{Name: "repo_full_name"},
			{Name: "number"},
		},
		DoUpdates: set,
	}).Create(link).Error
}

// SaveSyncedFields 按 (id, issue_id) 更新既有行的同步字段。不用 gorm Save：
// UPDATE 影响 0 行时它会退化 INSERT，把 sync 期间被并发 detach 的行原样插回。
// 行不存在时返回 gorm.ErrRecordNotFound 让调用方记失败。
func (r *IssuePullRequestRepository) SaveSyncedFields(link *model.IssuePullRequest) error {
	res := r.db.Model(&model.IssuePullRequest{}).
		Where("id = ? AND issue_id = ?", link.ID, link.IssueID).
		Updates(map[string]any{
			"repo_full_name": link.RepoFullName,
			"html_url":       link.HTMLURL,
			"title":          link.Title,
			"state":          link.State,
			"is_draft":       link.IsDraft,
			"author_login":   link.AuthorLogin,
			"head_ref":       link.HeadRef,
			"base_ref":       link.BaseRef,
			"merged_at":      link.MergedAt,
			"closed_at":      link.ClosedAt,
			"synced_at":      link.SyncedAt,
			"updated_at":     link.UpdatedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// FindByID 按行 ID 取关联，同时校验归属 issue_id。
func (r *IssuePullRequestRepository) FindByID(issueID, id string) (*model.IssuePullRequest, error) {
	var link model.IssuePullRequest
	if err := r.db.Where("issue_id = ? AND id = ?", issueID, id).First(&link).Error; err != nil {
		return nil, err
	}
	return &link, nil
}

// FindByUnique 按业务唯一键 (issue_id, provider, repo_full_name, number) 取关联。
func (r *IssuePullRequestRepository) FindByUnique(issueID, provider, repoFullName string, number int) (*model.IssuePullRequest, error) {
	var link model.IssuePullRequest
	err := r.db.Where("issue_id = ? AND provider = ? AND repo_full_name = ? AND number = ?",
		issueID, provider, repoFullName, number).First(&link).Error
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (r *IssuePullRequestRepository) ListByIssueID(issueID string) ([]model.IssuePullRequest, error) {
	var links []model.IssuePullRequest
	err := r.db.Where("issue_id = ?", issueID).
		Order("created_at ASC, id ASC").
		Find(&links).Error
	return links, err
}

func (r *IssuePullRequestRepository) Delete(issueID, id string) error {
	return r.db.Where("issue_id = ? AND id = ?", issueID, id).Delete(&model.IssuePullRequest{}).Error
}

// IssuePullRequestSummary 是列表项用的聚合计数。
type IssuePullRequestSummary struct {
	Total  int
	Open   int
	Merged int
}

// SummariesByIssueIDs 一次 GROUP BY 聚合多个 Issue 的 PR 计数，避免列表 N+1。
func (r *IssuePullRequestRepository) SummariesByIssueIDs(issueIDs []string) (map[string]IssuePullRequestSummary, error) {
	result := make(map[string]IssuePullRequestSummary, len(issueIDs))
	if len(issueIDs) == 0 {
		return result, nil
	}

	var rows []struct {
		IssueID string `gorm:"column:issue_id"`
		Total   int    `gorm:"column:total"`
		Open    int    `gorm:"column:open"`
		Merged  int    `gorm:"column:merged"`
	}
	err := r.db.Model(&model.IssuePullRequest{}).
		Select(`issue_id,
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN state = 'open' THEN 1 ELSE 0 END), 0) AS open,
			COALESCE(SUM(CASE WHEN state = 'merged' THEN 1 ELSE 0 END), 0) AS merged`).
		Where("issue_id IN ?", issueIDs).
		Group("issue_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.IssueID] = IssuePullRequestSummary{Total: row.Total, Open: row.Open, Merged: row.Merged}
	}
	return result, nil
}

// DeleteMissingSynced 删除该 Issue 下 link_origin=synced 且同步键不在 keepKeys 中的行；
// keepKeys 元素为 model.IssuePullRequest.SyncKey() 形式，空集表示删该 Issue 全部 synced 行。
// manual 行永不受此函数影响——手动 attach 的关联只能由用户显式解除。
func (r *IssuePullRequestRepository) DeleteMissingSynced(issueID string, keepKeys []string) error {
	query := r.db.Where("issue_id = ? AND link_origin = ?", issueID, model.IssuePullRequestLinkOriginSynced)
	if len(keepKeys) > 0 {
		query = query.Where("(provider || '|' || repo_full_name || '|' || number) NOT IN ?", keepKeys)
	}
	return query.Delete(&model.IssuePullRequest{}).Error
}
