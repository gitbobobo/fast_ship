package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UserAISettingRepository struct {
	db *gorm.DB
}

func NewUserAISettingRepository(db *gorm.DB) *UserAISettingRepository {
	return &UserAISettingRepository{db: db}
}

func (r *UserAISettingRepository) Get(userID string) (*model.UserAISetting, error) {
	var setting model.UserAISetting
	if err := r.db.Where("user_id = ?", userID).First(&setting).Error; err != nil {
		return nil, err
	}
	return &setting, nil
}

func (r *UserAISettingRepository) Upsert(setting *model.UserAISetting) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"api_host",
			"model",
			"api_key_encrypted",
			"updated_at",
		}),
	}).Create(setting).Error
}

func (r *UserAISettingRepository) Delete(userID string) error {
	return r.db.Where("user_id = ?", userID).Delete(&model.UserAISetting{}).Error
}
