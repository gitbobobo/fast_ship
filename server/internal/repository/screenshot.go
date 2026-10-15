package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ScreenshotRepository 承载 screenshot_screens 表存取。
// 标注的过期置位与批量动作要求与截图写路径同事务，这里只提供事务入口。
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

// FindScreenByVariant 按 (project_id, screen_key, platform, locale, theme) 找变体；
// NULL 维度与 NULL 相等（COALESCE 口径），与表达式唯一索引一致。
func (r *ScreenshotRepository) FindScreenByVariant(projectID, screenKey string, platform, locale, theme *string) (*model.ScreenshotScreen, error) {
	var screen model.ScreenshotScreen
	err := r.db.Where(
		"project_id = ? AND screen_key = ? AND COALESCE(platform,'') = ? AND COALESCE(locale,'') = ? AND COALESCE(theme,'') = ?",
		projectID, screenKey, derefString(platform), derefString(locale), derefString(theme),
	).First(&screen).Error
	if err != nil {
		return nil, err
	}
	return &screen, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ScreenshotScreenFilters 是项目级截图列表的可选过滤项；
// 各维度的多值与「未标注」桶（IncludeNull）按 OR 并入同维度条件。
type ScreenshotScreenFilters struct {
	Platforms       []string
	IncludeNoPlat   bool
	Locales         []string
	IncludeNoLocale bool
	Themes          []string
	IncludeNoTheme  bool
}

func (r *ScreenshotRepository) ListScreensByProject(projectID string, filters ScreenshotScreenFilters) ([]model.ScreenshotScreen, error) {
	query := r.db.Where("project_id = ?", projectID)
	query = applyDimensionFilter(query, "platform", filters.Platforms, filters.IncludeNoPlat)
	query = applyDimensionFilter(query, "locale", filters.Locales, filters.IncludeNoLocale)
	query = applyDimensionFilter(query, "theme", filters.Themes, filters.IncludeNoTheme)
	var screens []model.ScreenshotScreen
	err := query.Order("uploaded_at DESC, id ASC").Find(&screens).Error
	return screens, err
}

// applyDimensionFilter 拼一个维度的多值过滤：命中任一枚举值或（要求时）为 NULL。
// 值走参数占位符，不由字符串拼接注入；空过滤不产生条件。
func applyDimensionFilter(query *gorm.DB, column string, values []string, includeNull bool) *gorm.DB {
	switch {
	case len(values) > 0 && includeNull:
		return query.Where("("+column+" IN ? OR "+column+" IS NULL)", values)
	case len(values) > 0:
		return query.Where(column+" IN ?", values)
	case includeNull:
		return query.Where(column + " IS NULL")
	default:
		return query
	}
}

func (r *ScreenshotRepository) UpdateScreenByMap(id string, fields map[string]interface{}) error {
	return r.db.Model(&model.ScreenshotScreen{}).Where("id = ?", id).Updates(fields).Error
}

// MarkScreenAnnotationsOutdated 把界面上全部 open 标注置为过期（截图覆盖后调用）。
// resolved 不动；与覆盖写同事务，走事务闭包内的 repo 句柄。
func (r *ScreenshotRepository) MarkScreenAnnotationsOutdated(screenID string) error {
	return r.db.Model(&model.ScreenshotAnnotation{}).
		Where("screen_id = ? AND status = ?", screenID, "open").
		Update("outdated", true).Error
}

func (r *ScreenshotRepository) DeleteScreen(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.ScreenshotScreen{}).Error
}
