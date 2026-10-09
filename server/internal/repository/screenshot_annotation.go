package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// ScreenshotAnnotationRepository 承载 screenshot_annotations 表存取；
// LoadRelated 顺带批量取回组装响应所需的版本/界面/最新版本信息，
// 使 Issue 详情内嵌与项目级列表共用同一组装口径。
type ScreenshotAnnotationRepository struct {
	db *gorm.DB
}

func NewScreenshotAnnotationRepository(db *gorm.DB) *ScreenshotAnnotationRepository {
	return &ScreenshotAnnotationRepository{db: db}
}

// ScreenshotAnnotationFilters 是项目级列表的可选过滤项，空值不参与过滤。
type ScreenshotAnnotationFilters struct {
	Status   string
	IssueID  string
	ScreenID string
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

// ListByProject 返回项目下全部标注（含旧版本上的），按创建先后升序；rowid 兜底同毫秒并列。
func (r *ScreenshotAnnotationRepository) ListByProject(projectID string, filters ScreenshotAnnotationFilters) ([]model.ScreenshotAnnotation, error) {
	query := r.db.Where("project_id = ?", projectID)
	if filters.Status != "" {
		query = query.Where("status = ?", filters.Status)
	}
	if filters.IssueID != "" {
		query = query.Where("issue_id = ?", filters.IssueID)
	}
	if filters.ScreenID != "" {
		query = query.Where("screen_id = ?", filters.ScreenID)
	}
	var annotations []model.ScreenshotAnnotation
	err := query.Order("created_at ASC, rowid ASC").Find(&annotations).Error
	return annotations, err
}

func (r *ScreenshotAnnotationRepository) ListByIssueID(issueID string) ([]model.ScreenshotAnnotation, error) {
	var annotations []model.ScreenshotAnnotation
	err := r.db.Where("issue_id = ?", issueID).
		Order("created_at ASC, rowid ASC").
		Find(&annotations).Error
	return annotations, err
}

// ScreenshotAnnotationRelated 是一批标注组装响应所需的关联行集合：
// 所在版本（原图地址/尺寸）、所属界面（key/标题/分组）与各界面当前最新版本。
type ScreenshotAnnotationRelated struct {
	Versions map[string]model.ScreenshotVersion
	Screens  map[string]model.ScreenshotScreen
	Latest   map[string]model.ScreenshotVersion
}

// LoadRelated 批量取回 annotations 涉及的版本/界面行与各界面最新版本。
// 查询截图库两表——标注响应就是「标注行 + 版本/界面上下文」的组装物，放本 repo 内聚。
func (r *ScreenshotAnnotationRepository) LoadRelated(annotations []model.ScreenshotAnnotation) (*ScreenshotAnnotationRelated, error) {
	related := &ScreenshotAnnotationRelated{
		Versions: make(map[string]model.ScreenshotVersion),
		Screens:  make(map[string]model.ScreenshotScreen),
		Latest:   make(map[string]model.ScreenshotVersion),
	}
	if len(annotations) == 0 {
		return related, nil
	}

	versionIDs := make([]string, 0, len(annotations))
	screenIDs := make([]string, 0, len(annotations))
	seenVersion := make(map[string]struct{}, len(annotations))
	seenScreen := make(map[string]struct{}, len(annotations))
	for _, a := range annotations {
		if _, ok := seenVersion[a.VersionID]; !ok {
			seenVersion[a.VersionID] = struct{}{}
			versionIDs = append(versionIDs, a.VersionID)
		}
		if _, ok := seenScreen[a.ScreenID]; !ok {
			seenScreen[a.ScreenID] = struct{}{}
			screenIDs = append(screenIDs, a.ScreenID)
		}
	}

	var versions []model.ScreenshotVersion
	if err := r.db.Where("id IN ?", versionIDs).Find(&versions).Error; err != nil {
		return nil, err
	}
	for _, v := range versions {
		related.Versions[v.ID] = v
	}

	var screens []model.ScreenshotScreen
	if err := r.db.Where("id IN ?", screenIDs).Find(&screens).Error; err != nil {
		return nil, err
	}
	for _, s := range screens {
		related.Screens[s.ID] = s
	}

	// 与 ScreenshotRepository.LatestVersionsByScreenIDs 同口径：rowid 最大者为最新版本
	sub := r.db.Model(&model.ScreenshotVersion{}).
		Select("MAX(rowid)").
		Where("screen_id IN ?", screenIDs).
		Group("screen_id")
	var latest []model.ScreenshotVersion
	if err := r.db.Where("rowid IN (?)", sub).Find(&latest).Error; err != nil {
		return nil, err
	}
	for _, v := range latest {
		related.Latest[v.ScreenID] = v
	}

	return related, nil
}
