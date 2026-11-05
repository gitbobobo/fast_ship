package repository

import (
	"errors"

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

// syncedColumns 是 attach Upsert 冲突更新里受 synced_at 守卫约束的远端快照列。
var syncedColumns = []string{
	"html_url", "title", "state", "is_draft", "author_login",
	"head_ref", "base_ref", "merged_at", "closed_at",
}

func (r *IssuePullRequestRepository) Create(link *model.IssuePullRequest) error {
	// synced_at 落库前归一 UTC：守卫的文本字典序比较依赖统一格式
	//（INT-175 R25 的前提在写入端强制化，不再只是注释约定）。
	link.SyncedAt = link.SyncedAt.UTC()
	return r.db.Create(link).Error
}

// Upsert 以业务唯一键 (issue_id, provider, host_key, repo_full_name, number) 原子
// 插入或刷新：不同 Forgejo 实例的同名 PR 天然是两行并存（host_key 在键内），
// 不需要任何覆盖守卫；同主机重复 attach 就是普通 upsert。冲突时更新同步字段
// 并保留既有主键；forge_integration_id 只在新值非空时刷新（重复 attach 换绑
// 时快照跟随本次解析；空快照的调用方——如未来的同步投影——不能把 manual 行
// 的快照抹掉，那会让 sync 永久报缺快照）。host_key 不在冲突更新列内——它参
// 与身份判定，不随写入漂移（集成改 base_url 由 sync 的主机比对拦下）。link_
// origin 只升不降——手动 attach 可把 synced 行升级为 manual，反向不允许（避
// 免同步投影把手动关联降级、继而被 DeleteMissingSynced 清掉）。快照列受
// synced_at 守卫（INT-188）：excluded.synced_at 更旧时保留库内值，attach 与
// 并发 sync/attach 交错时旧快照不再覆盖较新数据；守卫逐列走 CASE 而非整条
// DO UPDATE WHERE（INT-175 R13 的设计），link_origin 的 manual 升级与
// forge_integration_id 换绑（身份语义，重于修改时间）不被旧快照连带拦下。
func (r *IssuePullRequestRepository) Upsert(link *model.IssuePullRequest) error {
	// synced_at/updated_at 落库前归一 UTC（R25 前提在写入端强制化）：
	// excluded.synced_at 与库内列的文本字典序比较依赖统一格式。
	link.SyncedAt = link.SyncedAt.UTC()
	link.UpdatedAt = link.UpdatedAt.UTC()
	set := make(clause.Set, 0, len(syncedColumns)+4)
	for _, col := range syncedColumns {
		set = append(set, clause.Assignment{
			Column: clause.Column{Name: col},
			Value:  gorm.Expr("CASE WHEN excluded.synced_at >= issue_pull_requests.synced_at THEN excluded." + col + " ELSE issue_pull_requests." + col + " END"),
		})
	}
	// synced_at 取两者较大值：守卫命中时保留库内较新时间，不随旧快照回拨
	//（回拨会让后续 sync 的守卫比较失真）；updated_at 同口径——守卫拦下快
	// 照时行内容未变，不刷修改时间。守卫命中但 link_origin/集成换绑生效时
	// 行变了而 updated_at 不刷是有意取舍，与 SaveSyncedFields 口径一致。
	set = append(set,
		clause.Assignment{
			Column: clause.Column{Name: "synced_at"},
			Value:  gorm.Expr("CASE WHEN excluded.synced_at >= issue_pull_requests.synced_at THEN excluded.synced_at ELSE issue_pull_requests.synced_at END"),
		},
		clause.Assignment{
			Column: clause.Column{Name: "updated_at"},
			Value:  gorm.Expr("CASE WHEN excluded.synced_at >= issue_pull_requests.synced_at THEN excluded.updated_at ELSE issue_pull_requests.updated_at END"),
		},
		clause.Assignment{
			Column: clause.Column{Name: "forge_integration_id"},
			Value:  gorm.Expr("CASE WHEN excluded.forge_integration_id <> '' THEN excluded.forge_integration_id ELSE issue_pull_requests.forge_integration_id END"),
		},
		clause.Assignment{
			Column: clause.Column{Name: "link_origin"},
			Value:  gorm.Expr("CASE WHEN excluded.link_origin = 'manual' THEN 'manual' ELSE issue_pull_requests.link_origin END"),
		})
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "issue_id"},
			{Name: "provider"},
			{Name: "host_key"},
			{Name: "repo_full_name"},
			{Name: "number"},
		},
		DoUpdates: set,
	}).Create(link).Error
}

// SaveSyncedFields 按 (id, issue_id) 更新既有行的同步字段。不用 gorm Save：
// UPDATE 影响 0 行时它会退化 INSERT，把 sync 期间被并发 detach 的行原样插回。
// 行不存在时返回 gorm.ErrRecordNotFound 让调用方记失败。
//
// 带 synced_at 时间守卫（INT-175 R8）：拉取与写回拆成两段后，同一个 Issue
// 的两个并发 sync 可能按发起顺序倒置落库——慢批次不能把后写入的更新数据
// 覆盖回旧值。0 行命中有两种含义：行已被解除（ErrRecordNotFound）或库内
// synced_at 已更新（ErrSyncedAtStale），由调用方 FindByID 区分。
// 带 synced_at 时间守卫（INT-175 R8）：拉取与写回拆成两段后，同一个 Issue
// 的两个并发 sync 可能按发起顺序倒置落库——慢批次不能把后写入的更新数据
// 覆盖回旧值。0 行命中有两种含义：行已被解除（ErrRecordNotFound）或库内
// synced_at 已更新（ErrSyncedAtStale），由调用方 FindByID 区分。
// 注意：该比较在 SQLite 里是文本字典序——写入端已把 synced_at 统一归一
// UTC（见各写方法），混入其它时区/格式的路径会被归一拉回，不再依赖调用
// 方自觉（R25 前提强制化，INT-188 审查）。
var ErrSyncedAtStale = errors.New("issue_pull_requests.synced_at 已有更新数据")

func (r *IssuePullRequestRepository) SaveSyncedFields(link *model.IssuePullRequest) error {
	link.SyncedAt = link.SyncedAt.UTC()
	link.UpdatedAt = link.UpdatedAt.UTC()
	res := r.db.Model(&model.IssuePullRequest{}).
		Where("id = ? AND issue_id = ? AND synced_at <= ?", link.ID, link.IssueID, link.SyncedAt).
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
		var current model.IssuePullRequest
		if err := r.db.Select("id").Where("id = ? AND issue_id = ?", link.ID, link.IssueID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return gorm.ErrRecordNotFound
			}
			return err
		}
		return ErrSyncedAtStale
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

// FindByUnique 按业务唯一键 (issue_id, provider, host_key, repo_full_name, number) 取关联。
func (r *IssuePullRequestRepository) FindByUnique(issueID, provider, hostKey, repoFullName string, number int) (*model.IssuePullRequest, error) {
	var link model.IssuePullRequest
	err := r.db.Where("issue_id = ? AND provider = ? AND host_key = ? AND repo_full_name = ? AND number = ?",
		issueID, provider, hostKey, repoFullName, number).First(&link).Error
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
// keepKeys 元素为 model.IssuePullRequest.SyncKey() 形式（含 host_key），空集表示删
// 该 Issue 全部 synced 行。manual 行永不受此函数影响——手动 attach 的关联只能由
// 用户显式解除。
func (r *IssuePullRequestRepository) DeleteMissingSynced(issueID string, keepKeys []string) error {
	query := r.db.Where("issue_id = ? AND link_origin = ?", issueID, model.IssuePullRequestLinkOriginSynced)
	if len(keepKeys) > 0 {
		query = query.Where("(provider || '|' || host_key || '|' || repo_full_name || '|' || number) NOT IN ?", keepKeys)
	}
	return query.Delete(&model.IssuePullRequest{}).Error
}
