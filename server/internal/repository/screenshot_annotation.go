package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ScreenshotAnnotationRepository 承载 screenshot_annotations 表存取；
// LoadRelated 批量取回组装响应所需的界面行，使 Issue 详情内嵌与项目级列表
// 共用同一组装口径。
type ScreenshotAnnotationRepository struct {
	db *gorm.DB
}

func NewScreenshotAnnotationRepository(db *gorm.DB) *ScreenshotAnnotationRepository {
	return &ScreenshotAnnotationRepository{db: db}
}

// ScreenshotAnnotationFilters 是标注集合过滤项，空值不参与过滤；
// 三个维度过滤 JOIN screenshot_screens 按截图上的维度匹配。
type ScreenshotAnnotationFilters struct {
	Status   string
	IssueID  string
	ScreenID string
	Outdated *bool

	Platforms       []string
	IncludeNoPlat   bool
	Locales         []string
	IncludeNoLocale bool
	Themes          []string
	IncludeNoTheme  bool
}

func (r *ScreenshotAnnotationRepository) Create(annotation *model.ScreenshotAnnotation) error {
	return r.db.Create(annotation).Error
}

func (r *ScreenshotAnnotationRepository) FindByID(id string) (*model.ScreenshotAnnotation, error) {
	var annotation model.ScreenshotAnnotation
	if err := r.db.Where("id = ?", id).First(&annotation).Error; err != nil {
		return nil, err
	}
	return &annotation, nil
}

func (r *ScreenshotAnnotationRepository) UpdateByMap(id string, fields map[string]interface{}) error {
	return r.db.Model(&model.ScreenshotAnnotation{}).Where("id = ?", id).Updates(fields).Error
}

func (r *ScreenshotAnnotationRepository) Delete(id string) error {
	return r.db.Where("id = ?", id).Delete(&model.ScreenshotAnnotation{}).Error
}

// applyAnnotationFilters 把过滤条件（含维度 JOIN）挂到标注查询上；
// projectID 为空串时不限定项目。包级函数：批量更新的事务闭包内也要复用
// 同一套过滤（INT-97 守卫禁止闭包内经仓库句柄调用方法）。
func applyAnnotationFilters(query *gorm.DB, projectID string, filters ScreenshotAnnotationFilters) *gorm.DB {
	if projectID != "" {
		query = query.Where("screenshot_annotations.project_id = ?", projectID)
	}
	if filters.Status != "" {
		query = query.Where("screenshot_annotations.status = ?", filters.Status)
	}
	if filters.IssueID != "" {
		query = query.Where("screenshot_annotations.issue_id = ?", filters.IssueID)
	}
	if filters.ScreenID != "" {
		query = query.Where("screenshot_annotations.screen_id = ?", filters.ScreenID)
	}
	if filters.Outdated != nil {
		query = query.Where("screenshot_annotations.outdated = ?", *filters.Outdated)
	}
	needsJoin := len(filters.Platforms) > 0 || filters.IncludeNoPlat ||
		len(filters.Locales) > 0 || filters.IncludeNoLocale ||
		len(filters.Themes) > 0 || filters.IncludeNoTheme
	if needsJoin {
		query = query.Joins("JOIN screenshot_screens ON screenshot_screens.id = screenshot_annotations.screen_id")
		query = applyDimensionFilter(query, "screenshot_screens.platform", filters.Platforms, filters.IncludeNoPlat)
		query = applyDimensionFilter(query, "screenshot_screens.locale", filters.Locales, filters.IncludeNoLocale)
		query = applyDimensionFilter(query, "screenshot_screens.theme", filters.Themes, filters.IncludeNoTheme)
	}
	return query
}

// ListByProject 返回项目下过滤后的标注，按创建先后升序；rowid 兜底同毫秒并列。
func (r *ScreenshotAnnotationRepository) ListByProject(projectID string, filters ScreenshotAnnotationFilters) ([]model.ScreenshotAnnotation, error) {
	query := applyAnnotationFilters(r.db.Model(&model.ScreenshotAnnotation{}), projectID, filters)
	var annotations []model.ScreenshotAnnotation
	err := query.Order("screenshot_annotations.created_at ASC, screenshot_annotations.rowid ASC").Find(&annotations).Error
	return annotations, err
}

func (r *ScreenshotAnnotationRepository) ListByIssueID(issueID string) ([]model.ScreenshotAnnotation, error) {
	var annotations []model.ScreenshotAnnotation
	err := r.db.Where("issue_id = ?", issueID).
		Order("created_at ASC, rowid ASC").
		Find(&annotations).Error
	return annotations, err
}

// BatchUpdateByFilters 对过滤集执行批量字段更新，返回更新行数。
// 供「全部确认保留 / 全部标记已解决」使用；SQLite 的 UPDATE 不带 JOIN，
// 先按过滤查出 ID 集再按 ID 更新。两条语句必须在同一事务里：生产库
// SetMaxOpenConns(1)，拆开的话 Pluck 的连接先归还，覆盖上传事务可以
// 插进来重置 outdated，随后按旧 ID 清零就把用户没看过的新图确认掉了。
func (r *ScreenshotAnnotationRepository) BatchUpdateByFilters(projectID string, filters ScreenshotAnnotationFilters, fields map[string]interface{}) (int64, error) {
	var updated int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		query := applyAnnotationFilters(tx.Model(&model.ScreenshotAnnotation{}), projectID, filters)
		var ids []string
		if err := query.Pluck("screenshot_annotations.id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		res := tx.Model(&model.ScreenshotAnnotation{}).Where("id IN ?", ids).Updates(fields)
		if res.Error != nil {
			return res.Error
		}
		updated = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

// ScreenshotAnnotationRelated 是一批标注组装响应所需的关联行集合：所属界面
// （key/标题/维度/原图尺寸）。
type ScreenshotAnnotationRelated struct {
	Screens map[string]model.ScreenshotScreen
}

// LoadRelated 批量取回 annotations 涉及的界面行。
// 查询截图库表——标注响应就是「标注行 + 界面上下文」的组装物，放本 repo 内聚。
func (r *ScreenshotAnnotationRepository) LoadRelated(annotations []model.ScreenshotAnnotation) (*ScreenshotAnnotationRelated, error) {
	related := &ScreenshotAnnotationRelated{
		Screens: make(map[string]model.ScreenshotScreen),
	}
	if len(annotations) == 0 {
		return related, nil
	}

	screenIDs := make([]string, 0, len(annotations))
	seenScreen := make(map[string]struct{}, len(annotations))
	for _, a := range annotations {
		if _, ok := seenScreen[a.ScreenID]; !ok {
			seenScreen[a.ScreenID] = struct{}{}
			screenIDs = append(screenIDs, a.ScreenID)
		}
	}

	var screens []model.ScreenshotScreen
	if err := r.db.Where("id IN ?", screenIDs).Find(&screens).Error; err != nil {
		return nil, err
	}
	for _, s := range screens {
		related.Screens[s.ID] = s
	}
	return related, nil
}
