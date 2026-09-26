package infer

import (
	"time"
)

// AutoscalingPolicy is the GORM model of the singleton autoscaling_policy
// table (feature #16, §4.2). It holds the global default autoscaling
// policy applied to new inference services. The row id is always 1.
type AutoscalingPolicy struct {
	// ID is the singleton row id (always 1).
	ID int `gorm:"primaryKey" json:"-"`
	// Enabled is the global default: autoscaling on for new services.
	Enabled bool `gorm:"not null;default:true" json:"enabled"`
	// MinReplicas is the lower replica bound (0-max).
	MinReplicas int `gorm:"not null;default:1" json:"min_replicas"`
	// MaxReplicas is the upper replica bound (min-100).
	MaxReplicas int `gorm:"not null;default:10" json:"max_replicas"`
	// TargetConcurrency is the HPA concurrency target (1-1000).
	TargetConcurrency int `gorm:"not null;default:32" json:"target_concurrency"`
	// ScaleToZero enables scale-to-zero (requires MinReplicas = 0).
	ScaleToZero bool `gorm:"not null;default:false" json:"scale_to_zero"`
	// CooldownSeconds is the downscale stabilization window (0-3600).
	CooldownSeconds int `gorm:"not null;default:300" json:"cooldown_seconds"`
	// UpdatedAt is the last save time.
	UpdatedAt time.Time `json:"-"`
}

// TableName returns the table name of AutoscalingPolicy.
func (AutoscalingPolicy) TableName() string { return "autoscaling_policy" }

// singletonPolicyID is the fixed id of the global-default row.
const singletonPolicyID = 1

// defaultAutoscalingPolicy returns the shipped defaults for the global
// default policy (feature #16, §4.2).
func defaultAutoscalingPolicy() *AutoscalingPolicy {
	return &AutoscalingPolicy{
		ID:                singletonPolicyID,
		Enabled:           true,
		MinReplicas:       1,
		MaxReplicas:       10,
		TargetConcurrency: 32,
		ScaleToZero:       false,
		CooldownSeconds:   300,
	}
}
