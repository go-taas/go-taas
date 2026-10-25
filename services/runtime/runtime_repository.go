package runtime

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
)

// RuntimeRepository persists the singleton serving-scheme setting.
type RuntimeRepository struct { //nolint:revive // runtime.RuntimeRepository is the documented domain name
	db *database.Manager
}

// NewRuntimeRepository constructs a RuntimeRepository bound to a GORM
// database.
func NewRuntimeRepository(db *gorm.DB) *RuntimeRepository {
	return &RuntimeRepository{db: database.NewManager(db)}
}

// GetScheme returns the persisted scheme, defaulting to plain when no
// singleton row exists (AD6). The returned setting always carries a
// scheme; a missing row yields a fresh plain default with a zero update
// time.
func (r *RuntimeRepository) GetScheme(ctx context.Context) (*RuntimeSetting, error) {
	var row RuntimeSetting
	err := r.db.DB(ctx).Where("id = ?", runtimeSettingID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &RuntimeSetting{ID: runtimeSettingID, Scheme: SchemePlain}, nil
	}
	if err != nil {
		return nil, err
	}
	if row.Scheme == "" {
		row.Scheme = SchemePlain
	}
	return &row, nil
}

// UpdateScheme upserts the singleton row and returns it.
func (r *RuntimeRepository) UpdateScheme(ctx context.Context, scheme, updatedBy string) (*RuntimeSetting, error) {
	now := time.Now().UTC()
	var out *RuntimeSetting
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		var existing RuntimeSetting
		err := r.db.DB(ctx).Where("id = ?", runtimeSettingID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row := &RuntimeSetting{
				ID:        runtimeSettingID,
				Scheme:    scheme,
				UpdatedBy: updatedBy,
				UpdatedAt: now,
				CreatedAt: now,
			}
			if err := r.db.DB(ctx).Create(row).Error; err != nil {
				return err
			}
			out = row
			return nil
		}
		if err != nil {
			return err
		}
		if err := r.db.DB(ctx).Model(&RuntimeSetting{}).
			Where("id = ?", runtimeSettingID).
			Updates(map[string]any{
				"scheme":     scheme,
				"updated_by": updatedBy,
				"updated_at": now,
			}).Error; err != nil {
			return err
		}
		out = &RuntimeSetting{
			ID:        runtimeSettingID,
			Scheme:    scheme,
			UpdatedBy: updatedBy,
			UpdatedAt: now,
			CreatedAt: existing.CreatedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetPlatformSettings returns the singleton platform_settings row,
// defaulting to an empty setting (https, no hostname) when no row exists.
func (r *RuntimeRepository) GetPlatformSettings(ctx context.Context) (*PlatformSetting, error) {
	var row PlatformSetting
	err := r.db.DB(ctx).Where("id = ?", platformSettingID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &PlatformSetting{ID: platformSettingID, InferenceGatewayScheme: GatewaySchemeHTTPS}, nil
	}
	if err != nil {
		return nil, err
	}
	if row.InferenceGatewayScheme == "" {
		row.InferenceGatewayScheme = GatewaySchemeHTTPS
	}
	return &row, nil
}

// UpdatePlatformSettings upserts the singleton row and returns it.
func (r *RuntimeRepository) UpdatePlatformSettings(ctx context.Context, setting *PlatformSetting) (*PlatformSetting, error) {
	now := time.Now().UTC()
	var out *PlatformSetting
	err := r.db.WithinTx(ctx, func(ctx context.Context) error {
		var existing PlatformSetting
		err := r.db.DB(ctx).Where("id = ?", platformSettingID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row := &PlatformSetting{
				ID:                          platformSettingID,
				InferenceGatewayHostname:    setting.InferenceGatewayHostname,
				InferenceGatewayScheme:      setting.InferenceGatewayScheme,
				InferenceGatewayInternalURL: setting.InferenceGatewayInternalURL,
				UpdatedBy:                   setting.UpdatedBy,
				UpdatedAt:                   now,
				CreatedAt:                   now,
			}
			if err := r.db.DB(ctx).Create(row).Error; err != nil {
				return err
			}
			out = row
			return nil
		}
		if err != nil {
			return err
		}
		if err := r.db.DB(ctx).Model(&PlatformSetting{}).
			Where("id = ?", platformSettingID).
			Updates(map[string]any{
				"inference_gateway_hostname":     setting.InferenceGatewayHostname,
				"inference_gateway_scheme":       setting.InferenceGatewayScheme,
				"inference_gateway_internal_url": setting.InferenceGatewayInternalURL,
				"updated_by":                     setting.UpdatedBy,
				"updated_at":                     now,
			}).Error; err != nil {
			return err
		}
		out = &PlatformSetting{
			ID:                          platformSettingID,
			InferenceGatewayHostname:    setting.InferenceGatewayHostname,
			InferenceGatewayScheme:      setting.InferenceGatewayScheme,
			InferenceGatewayInternalURL: setting.InferenceGatewayInternalURL,
			UpdatedBy:                   setting.UpdatedBy,
			UpdatedAt:                   now,
			CreatedAt:                   existing.CreatedAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
