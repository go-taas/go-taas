// Inference engine parameter templates.
//
// A template is a named set of engine arguments plus the deployment context it
// applies to (engine, model, topology, accelerator). The deploy form loads the
// matching template instead of making the operator retype a dozen flags, and
// an operator can keep one template per tuned combination:
//
//	name                     engine  model    topology   accelerator  card
//	Qwen2.5-0.5B · 单机       vllm    qwen     single     nvidia       4090
//	vLLM 通用 · 80G           vllm    (any)    (any)      nvidia
//	SGLang PD · 双机          sglang  (any)    pd         nvidia
//
// An empty context field matches every value, so a template can be as specific
// as one model+card or as general as "vLLM defaults".

package infer

import (
	"time"

	"gorm.io/datatypes"
)

// EngineParamTemplate is the GORM model of engine_param_templates. Templates
// belong to the platform: only a platform administrator manages them, because
// the whole fleet's engines are a platform concern.
type EngineParamTemplate struct {
	// ID is the server-generated UUID exposed as template_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// Name is the human-readable, unique template name.
	Name string `gorm:"size:128;not null;uniqueIndex:idx_engine_param_templates_name"`
	// Description is free text.
	Description string `gorm:"size:1024;not null;default:''"`
	// Engine is the serving engine the parameters belong to (vllm, sglang).
	Engine string `gorm:"size:64;not null;index"`
	// ModelID narrows the template to one catalog model; empty matches any.
	ModelID string `gorm:"type:uuid;not null;default:'';index"`
	// Topology narrows it to a serving layout; empty matches any.
	Topology string `gorm:"size:16;not null;default:''"`
	// Accelerator / AcceleratorType narrow it to hardware; empty matches any.
	Accelerator     string `gorm:"size:32;not null;default:''"`
	AcceleratorType string `gorm:"size:64;not null;default:''"`
	// EngineArgs are the shared flags as a JSON object.
	EngineArgs datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'"`
	// ExtraArgs are free-form arguments as a JSON array.
	ExtraArgs datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'"`
	// RoleEngineArgs are the per-role overrides as a JSON array.
	RoleEngineArgs datatypes.JSON `gorm:"type:jsonb;not null;default:'[]'"`
	// UpdatedBy is the acting administrator's label.
	UpdatedBy string `gorm:"size:255;not null;default:''"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName returns the table name of EngineParamTemplate.
func (EngineParamTemplate) TableName() string { return "engine_param_templates" }
