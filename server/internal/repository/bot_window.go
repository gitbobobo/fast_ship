package repository

import (
	"errors"

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
// 旧值整体被新探测结果替换。
func (r *BotWindowRepository) Upsert(row *model.BotModelWindow) error {
	if row.BaseURL == "" || row.Model == "" {
		return errors.New("bot window upsert: base_url 与 model 必填")
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "base_url"}, {Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"context_window", "max_output_tokens", "source", "detail", "fetched_at", "updated_at",
		}),
	}).Create(row).Error
}
