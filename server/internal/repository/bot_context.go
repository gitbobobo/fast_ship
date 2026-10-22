package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotContextRepository 管理持久投影状态与压缩最终记录。投影状态每机器人
// 一行，发布必须 CAS（旧 generation）；压缩记录 append-only。

type BotContextRepository struct {
	db *gorm.DB
}

func NewBotContextRepository(db *gorm.DB) *BotContextRepository {
	return &BotContextRepository{db: db}
}

// FindTx 取当前投影状态；尚无返回 gorm.ErrRecordNotFound（首代视为
// generation 0、无摘要、无折叠）。
func (r *BotContextRepository) FindTx(tx *gorm.DB, botID string) (*model.BotContextState, error) {
	var row model.BotContextState
	if err := tx.Where("bot_id = ?", botID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Find 同 FindTx，事务外读取。
func (r *BotContextRepository) Find(botID string) (*model.BotContextState, error) {
	return r.FindTx(r.db, botID)
}

// PublishTx 在事务内发布新投影：CAS 旧 generation 建行或推进行，守护触发器
// 校验 generation 恰好 +1 与摘要水位不后退。
func (r *BotContextRepository) PublishTx(
	tx *gorm.DB,
	botID string,
	prevGeneration int64,
	summaryThroughSeq int64,
	summaryText, identifierManifestJSON, foldedResultsJSON string,
	now time.Time,
) (int64, error) {
	next := prevGeneration + 1
	if prevGeneration == 0 {
		// 首次发布：建行（generation 1）。唯一主键兜底并发；已存在则 CAS 落空。
		// LifecycleRenderVersion 同步写 1（R6）：发布后仍留在投影里的旧
		// toolset_updated 按 user 角色发送。
		row := &model.BotContextState{
			BotID:                  botID,
			Generation:             next,
			SummaryThroughSeq:      summaryThroughSeq,
			SummaryText:            summaryText,
			IdentifierManifestJSON: identifierManifestJSON,
			FoldedResultsJSON:      foldedResultsJSON,
			LifecycleRenderVersion: 1,
			UpdatedAt:              now,
		}
		if err := tx.Create(row).Error; err != nil {
			return 0, err
		}
		return next, nil
	}
	res := tx.Model(&model.BotContextState{}).
		Where("bot_id = ? AND generation = ?", botID, prevGeneration).
		Updates(map[string]interface{}{
			"generation":               next,
			"summary_through_seq":      summaryThroughSeq,
			"summary_text":             summaryText,
			"identifier_manifest_json": identifierManifestJSON,
			"folded_results_json":      foldedResultsJSON,
			"lifecycle_render_version": 1,
			"updated_at":               now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, ErrContextGenerationConflict
	}
	return next, nil
}

// CreateCompactionTx 在事务内落一个压缩周期的最终记录（每维护运行至多一条，
// 唯一索引兜底）。
func (r *BotContextRepository) CreateCompactionTx(tx *gorm.DB, row *model.BotCompaction) error {
	return tx.Create(row).Error
}

// ErrContextGenerationConflict 投影发布 CAS 落空（并发发布或已被推进）。
var ErrContextGenerationConflict = errors.New("bot context generation conflict")
