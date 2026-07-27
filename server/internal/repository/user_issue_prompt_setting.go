package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UserIssuePromptSettingRepository struct {
	db *gorm.DB
}

func NewUserIssuePromptSettingRepository(db *gorm.DB) *UserIssuePromptSettingRepository {
	return &UserIssuePromptSettingRepository{db: db}
}

func (r *UserIssuePromptSettingRepository) Get(userID string) (*model.UserIssuePromptSetting, error) {
	var setting model.UserIssuePromptSetting
	if err := r.db.Where("user_id = ?", userID).First(&setting).Error; err != nil {
		return nil, err
	}
	return &setting, nil
}

func (r *UserIssuePromptSettingRepository) Upsert(setting *model.UserIssuePromptSetting) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"prompts",
			"updated_at",
		}),
	}).Create(setting).Error
}
