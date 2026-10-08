package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ScreenshotRepository 同时承载 screen 与 version 两表的存取：
// screen 的版本增删要求同事务重算冗余统计，单一 repo 便于共享 Transaction。
type ScreenshotRepository struct {
	db *gorm.DB
}

func NewScreenshotRepository(db *gorm.DB) *ScreenshotRepository {
	return &ScreenshotRepository{db: db}
}

func (r *ScreenshotRepository) Transaction(fn func(txRepo *ScreenshotRepository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return fn(&ScreenshotRepository{db: tx})
	})
}

func (r *ScreenshotRepository) CreateScreen(screen *model.ScreenshotScreen) error {
	return r.db.Create(screen).Error
}

func (r *ScreenshotRepository) FindScreenByID(id string) (*model.ScreenshotScreen, error) {
	var screen model.ScreenshotScreen
	err := r.db.Where("id = ?", id).First(&screen).Error
	if err != nil {
		return nil, err
	}
	return &screen, nil
}

func (r *ScreenshotRepository) FindScreenByKey(projectID, screenKey string) (*model.ScreenshotScreen, error) {
	var screen model.ScreenshotScreen
	err := r.db.Where("project_id = ? AND screen_key = ?", projectID, screenKey).First(&screen).Error
	if err != nil {
		return nil, err
	}
	return &screen, nil
}

func (r *ScreenshotRepository) ListScreensByProject(projectID string) ([]model.ScreenshotScreen, error) {
	var screens []model.ScreenshotScreen
	err := r.db.Where("project_id = ?", projectID).
		Order("last_uploaded_at DESC, id ASC").
		Find(&screens).Error
	return screens, err
}

func (r *ScreenshotRepository) UpdateScreenByMap(id string, fields map[string]interface{}) error {
	return r.db.Model(&model.ScreenshotScreen{}).Where("id = ?", id).Updates(fields).Error
}

func (r *ScreenshotRepository) DeleteScreen(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.ScreenshotScreen{}).Error
}

func (r *ScreenshotRepository) CreateVersion(version *model.ScreenshotVersion) error {
	return r.db.Create(version).Error
}

func (r *ScreenshotRepository) FindVersionByID(id string) (*model.ScreenshotVersion, error) {
	var version model.ScreenshotVersion
	err := r.db.Where("id = ?", id).First(&version).Error
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// 「最新版本」统一按 rowid（插入顺序）判定：uploaded_at 取自事务开始时间，
// 写锁等待或时钟回拨下可能与提交顺序不一致；rowid 才是真实的落库先后。
func (r *ScreenshotRepository) ListVersionsByScreenID(screenID string) ([]model.ScreenshotVersion, error) {
	var versions []model.ScreenshotVersion
	err := r.db.Where("screen_id = ?", screenID).
		Order("rowid DESC").
		Find(&versions).Error
	return versions, err
}

func (r *ScreenshotRepository) FindLatestVersionByScreenID(screenID string) (*model.ScreenshotVersion, error) {
	var version model.ScreenshotVersion
	err := r.db.Where("screen_id = ?", screenID).
		Order("rowid DESC").
		First(&version).Error
	if err != nil {
		return nil, err
	}
	return &version, nil
}

// LatestVersionsByScreenIDs 一次查询取回多个 screen 各自最新版本（rowid 最大者），
// 返回 screen_id → version 的映射；无版本的 screen 不在结果中。
func (r *ScreenshotRepository) LatestVersionsByScreenIDs(screenIDs []string) (map[string]model.ScreenshotVersion, error) {
	latest := make(map[string]model.ScreenshotVersion, len(screenIDs))
	if len(screenIDs) == 0 {
		return latest, nil
	}

	// 版本全量保留，rowid 最大者即最新插入的版本；
	// 直接在 SQL 里按 screen 取最新一行，避免把全量历史拉回内存排序
	sub := r.db.Model(&model.ScreenshotVersion{}).
		Select("MAX(rowid)").
		Where("screen_id IN ?", screenIDs).
		Group("screen_id")
	var versions []model.ScreenshotVersion
	err := r.db.Where("rowid IN (?)", sub).Find(&versions).Error
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		latest[version.ScreenID] = version
	}
	return latest, nil
}

func (r *ScreenshotRepository) CountVersionsByScreenID(screenID string) (int64, error) {
	var count int64
	err := r.db.Model(&model.ScreenshotVersion{}).Where("screen_id = ?", screenID).Count(&count).Error
	return count, err
}

func (r *ScreenshotRepository) DeleteVersion(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.ScreenshotVersion{}).Error
}

func (r *ScreenshotRepository) DeleteVersionsByScreenID(screenID string) error {
	return r.db.Where("screen_id = ?", screenID).Delete(&model.ScreenshotVersion{}).Error
}
