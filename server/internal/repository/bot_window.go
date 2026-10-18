package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BotWindowRepository 是模型窗口元数据缓存（INT-115 需求一）的仓库：
// 按「规范化地址 + 模型名」读写，命中与否由调用方按 TTL 判断。
type BotWindowRepository struct {
	db *gorm.DB
}

func NewBotWindowRepository(db *gorm.DB) *BotWindowRepository {
	return &BotWindowRepository{db: db}
}

// FindByAddrModel 精确命中一行缓存；未命中返回 gorm.ErrRecordNotFound。
func (r *BotWindowRepository) FindByAddrModel(baseURL, modelName string) (*model.BotModelWindow, error) {
	var row model.BotModelWindow
	if err := r.db.Where("base_url = ? AND model = ?", baseURL, modelName).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// Upsert 按 (base_url, model) 唯一索引覆盖写入：重复探测不产生第二行，
// 旧值整体被新探测结果替换。failed_at 在赋值列里——成功写把 nil 写成 NULL，
// 清除既有的失败退避时间。
func (r *BotWindowRepository) Upsert(row *model.BotModelWindow) error {
	if row.BaseURL == "" || row.Model == "" {
		return errors.New("bot window upsert: base_url 与 model 必填")
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "base_url"}, {Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"context_window", "max_output_tokens", "source", "detail", "fetched_at", "failed_at", "updated_at",
		}),
	}).Create(row).Error
}

// MarkProbeFailed 在已有成功行（context_window > 0）上记录失败退避时间：
// 只碰 failed_at/updated_at，不改窗口、输出与原始 fetched_at——成功数据保持
// 原值，新鲜度仍按 FetchedAt 判断。命中成功行返回 true；失败行与无行返回
// false（它们由 UpsertFailure 插入或刷新，那里也记失败时间）。
func (r *BotWindowRepository) MarkProbeFailed(baseURL, modelName string, at time.Time) (bool, error) {
	res := r.db.Model(&model.BotModelWindow{}).
		Where("base_url = ? AND model = ? AND context_window > 0", baseURL, modelName).
		Updates(map[string]interface{}{"failed_at": at, "updated_at": at})
	return res.RowsAffected > 0, res.Error
}

// UpsertFailure 按 (base_url, model) 条件写失败标记（INT-115 F5）：无行则
// 插入；有行时仅当前行是失败/空标记（context_window = 0）才覆盖。条件在
// 数据库层原子生效——「失败不能覆盖成功」不再依赖先查再写的检查顺序，并发
// 下「失败处理读到无行 → 成功写入 → 失败处理覆盖」的竞争窗口不存在；有效
// 成功行（context_window > 0）永远保持原值（它们的失败时间走 MarkProbeFailed）。
// 赋值列含 failed_at：失败行同时刷新失败退避时间。
func (r *BotWindowRepository) UpsertFailure(row *model.BotModelWindow) error {
	if row.BaseURL == "" || row.Model == "" {
		return errors.New("bot window upsert failure: base_url 与 model 必填")
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "base_url"}, {Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"context_window", "max_output_tokens", "source", "detail", "fetched_at", "failed_at", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{
			gorm.Expr("bot_model_windows.context_window = 0"),
		}},
	}).Create(row).Error
}
