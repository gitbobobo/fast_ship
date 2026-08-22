package model

import "time"

type VersionStatus string

const (
	VersionStatusPending VersionStatus = "pending"
	VersionStatusShipped VersionStatus = "shipped"
)

type ShipStatus string

const (
	ShipStatusIdle       ShipStatus = ""
	ShipStatusInProgress ShipStatus = "in_progress"
	ShipStatusFailed     ShipStatus = "failed"
	ShipStatusCompleted  ShipStatus = "completed"
)

type ShipStage string

const (
	ShipStagePreCheck      ShipStage = "precheck"
	ShipStageCreateTag     ShipStage = "create_tag"
	ShipStageCreateRelease ShipStage = "create_release"
	ShipStageUploadAssets  ShipStage = "upload_assets"
	ShipStageFinalize      ShipStage = "finalize"
)

type Version struct {
	ID               string        `gorm:"type:text;primaryKey" json:"id"`
	ProjectID        string        `gorm:"type:text;not null;index" json:"project_id"`
	VersionNumber    string        `gorm:"type:text;not null" json:"version_number"`
	Status           VersionStatus `gorm:"type:text;not null;default:pending;index" json:"status"`
	ReleaseNotes     string        `gorm:"type:text" json:"release_notes"`
	TargetCommitish  string        `gorm:"type:text" json:"target_commitish"`
	GithubReleaseURL string        `gorm:"type:text" json:"github_release_url"`
	ErrorLog         string        `gorm:"type:text" json:"error_log"`
	ShipStatus       ShipStatus    `gorm:"type:text" json:"ship_status"`
	ShipStage        ShipStage     `gorm:"type:text" json:"ship_stage"`
	ShipMessage      string        `gorm:"type:text" json:"ship_message"`
	ShipHooksStatus  string        `gorm:"type:text;default:pending" json:"ship_hooks_status"`
	CreatedAt        time.Time     `gorm:"not null" json:"created_at"`
	ShippedAt        *time.Time    `json:"shipped_at"`

	Project   Project    `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
	Artifacts []Artifact `gorm:"foreignKey:VersionID" json:"artifacts,omitempty"`
}

func (Version) TableName() string {
	return "versions"
}

// UniqueIndex: project_id + version_number
