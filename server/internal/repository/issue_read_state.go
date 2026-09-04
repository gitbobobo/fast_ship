package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IssueReadStateRepository 维护「用户 × Issue」的已读评论水位。
type IssueReadStateRepository struct {
	db *gorm.DB
}

func NewIssueReadStateRepository(db *gorm.DB) *IssueReadStateRepository {
	return &IssueReadStateRepository{db: db}
}

func (r *IssueReadStateRepository) Get(userID, issueID string) (*model.IssueReadState, error) {
	var state model.IssueReadState
	if err := r.db.Where("user_id = ? AND issue_id = ?", userID, issueID).First(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

// Advance 只让水位前进：冲突更新仅在 excluded.last_read_at 更晚时生效，避免旧请求回拨。
func (r *IssueReadStateRepository) Advance(userID, issueID string, at time.Time) error {
	now := time.Now().UTC()
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "issue_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_read_at": gorm.Expr("excluded.last_read_at"),
			"updated_at":   gorm.Expr("excluded.updated_at"),
		}),
		Where: clause.Where{
			Exprs: []clause.Expression{
				clause.Expr{SQL: "excluded.last_read_at > issue_read_states.last_read_at"},
			},
		},
	}).Create(&model.IssueReadState{
		UserID:     userID,
		IssueID:    issueID,
		LastReadAt: at,
		UpdatedAt:  now,
	}).Error
}

// CountUnreadByIssueIDs 统计每个 Issue 上比水位更新的 GitHub 评论数。
// 没有水位记录的 Issue（新同步进来、从未打开过）的非钩子评论全部算未读。
func (r *IssueReadStateRepository) CountUnreadByIssueIDs(userID string, issueIDs []string) (map[string]int64, error) {
	result := make(map[string]int64, len(issueIDs))
	if len(issueIDs) == 0 {
		return result, nil
	}

	var rows []struct {
		IssueID string `gorm:"column:issue_id"`
		Total   int64  `gorm:"column:total"`
	}
	err := r.db.Raw(`
		SELECT c.issue_id AS issue_id, COUNT(*) AS total
		FROM issue_comments c
		JOIN issues i ON i.id = c.issue_id
		LEFT JOIN issue_read_states r ON r.issue_id = c.issue_id AND r.user_id = ?
		WHERE c.issue_id IN ?
		  AND i.source = ?
		  AND c.source = ?
		  AND c.body NOT LIKE ?
		  AND (r.last_read_at IS NULL OR datetime(c.created_at) > datetime(r.last_read_at))
		GROUP BY c.issue_id
	`,
		userID,
		issueIDs,
		string(model.IssueSourceGitHub),
		string(model.IssueSourceGitHub),
		"%"+model.FastShipHookCommentMarker+"%",
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.IssueID] = row.Total
	}
	return result, nil
}
