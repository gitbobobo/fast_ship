package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"
)

type IssuePromptItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// IssuePromptItems 以 JSON 文本存取于 prompts 列。
type IssuePromptItems []IssuePromptItem

func (items IssuePromptItems) Value() (driver.Value, error) {
	if items == nil {
		return "[]", nil
	}
	bytes, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return string(bytes), nil
}

func (items *IssuePromptItems) Scan(src any) error {
	if items == nil {
		return errors.New("IssuePromptItems: Scan target is nil")
	}

	switch value := src.(type) {
	case nil:
		*items = nil
		return nil
	case []byte:
		if len(value) == 0 {
			*items = nil
			return nil
		}
		return json.Unmarshal(value, items)
	case string:
		if value == "" {
			*items = nil
			return nil
		}
		return json.Unmarshal([]byte(value), items)
	}

	return errors.New("IssuePromptItems: unsupported scan source type")
}

type UserIssuePromptSetting struct {
	UserID    string           `gorm:"type:text;primaryKey" json:"user_id"`
	Prompts   IssuePromptItems `gorm:"type:text;not null" json:"prompts"`
	CreatedAt time.Time        `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time        `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserIssuePromptSetting) TableName() string {
	return "user_issue_prompt_settings"
}
