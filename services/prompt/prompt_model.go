// Package prompt implements the prompt management & prompt library
// service: the prompt lifecycle, versioning, folders, usage counters,
// and the shared template library (feature #43).
package prompt

import "time"

// Prompt is the GORM model for the prompts table (AD2, §4.1).
type Prompt struct {
	PromptID       string     `gorm:"primaryKey;type:uuid"`
	OrganizationID string     `gorm:"size:64;not null;index:idx_prompts_org_created,priority:1"`
	Name           string     `gorm:"size:128;not null"`
	ModelID        string     `gorm:"size:64"`
	FolderID       *string    `gorm:"type:uuid;index"`
	ActiveVersion  int        `gorm:"not null;default:1"`
	TimesUsed      int64      `gorm:"not null;default:0"`
	LastUsedAt     *time.Time `gorm:"index"`
	CreatedAt      time.Time  `gorm:"not null;index:idx_prompts_org_created,priority:2"`
	UpdatedAt      time.Time  `gorm:"not null"`
}

// TableName overrides the default GORM table name.
func (Prompt) TableName() string { return "prompts" }

// PromptVersion is the GORM model for the prompt_versions table (AD3,
// §4.2).
type PromptVersion struct { //nolint:revive // prompt.PromptVersion is the documented domain name
	PromptID  string    `gorm:"type:uuid;not null;primaryKey"`
	Version   int       `gorm:"not null;primaryKey"`
	Content   string    `gorm:"type:text;not null"`
	ModelID   string    `gorm:"size:64"`
	Variables string    `gorm:"type:jsonb;not null;default:'[]'"`
	CreatedAt time.Time `gorm:"not null"`
	CreatedBy string    `gorm:"size:64;not null"`
}

// TableName overrides the default GORM table name.
func (PromptVersion) TableName() string { return "prompt_versions" }

// PromptFolder is the GORM model for the prompt_folders table (§4.3).
type PromptFolder struct { //nolint:revive // prompt.PromptFolder is the documented domain name
	FolderID       string    `gorm:"primaryKey;type:uuid"`
	OrganizationID string    `gorm:"size:64;not null;index"`
	Name           string    `gorm:"size:128;not null"`
	CreatedAt      time.Time `gorm:"not null"`
	// PromptCount is the number of prompts in the folder (computed, not
	// persisted).
	PromptCount int64 `gorm:"-"`
}

// TableName overrides the default GORM table name.
func (PromptFolder) TableName() string { return "prompt_folders" }

// PromptTemplate is the GORM model for the prompt_templates table (§4.4).
type PromptTemplate struct { //nolint:revive // prompt.PromptTemplate is the documented domain name
	TemplateID string    `gorm:"primaryKey;type:uuid"`
	Name       string    `gorm:"size:128;not null"`
	Content    string    `gorm:"type:text;not null"`
	ModelID    string    `gorm:"size:64"`
	Variables  string    `gorm:"type:jsonb;not null;default:'[]'"`
	TimesUsed  int64     `gorm:"not null;default:0"`
	CreatedAt  time.Time `gorm:"not null"`
	UpdatedAt  time.Time `gorm:"not null"`
}

// TableName overrides the default GORM table name.
func (PromptTemplate) TableName() string { return "prompt_templates" }

// PromptFilter scopes a prompt list query (AC5).
type PromptFilter struct { //nolint:revive // prompt.PromptFilter is the documented domain name
	OrganizationID string
	Search         string
	FolderID       string
	ModelID        string
	Offset         int
	Limit          int
}

// TemplateFilter scopes a template list query.
type TemplateFilter struct {
	Search  string
	ModelID string
	Offset  int
	Limit   int
}
