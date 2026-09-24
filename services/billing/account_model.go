package billing

import (
	"time"
)

// Account is the per-organization billing account — one row per org
// (AD1), in one of two modes: prepaid (a balance drawn down by
// settlement deductions) or postpaid (a monthly quota accumulating
// usage within the cycle). Balance and usage are integer minor units
// (AD2); concurrent writes are guarded by the optimistic version
// counter (AD11).
type Account struct {
	// ID is the server-generated UUID v4, exposed as account_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// OrganizationID is the owning organization; one account per org
	// (AD1).
	OrganizationID string `gorm:"size:64;not null;uniqueIndex"`
	// Mode is "prepaid" or "postpaid".
	Mode string `gorm:"size:16;not null"`
	// BalanceCents is the prepaid funds in minor units; may go <= 0
	// via settlement lag (AD3).
	BalanceCents int64 `gorm:"not null;default:0"`
	// MonthlyQuotaCents is the postpaid monthly cap; 0 = unlimited.
	MonthlyQuotaCents int64 `gorm:"not null;default:0"`
	// UsedThisCycleCents is the postpaid usage within the current
	// cycle.
	UsedThisCycleCents int64 `gorm:"not null;default:0"`
	// CycleStartedAt is the UTC month start the cycle covers, unix
	// seconds.
	CycleStartedAt int64 `gorm:"not null"`
	// OverdrawPolicy is the postpaid cap enforcement: "block" or
	// "warn".
	OverdrawPolicy string `gorm:"size:8;not null;default:block"`
	// Currency is the platform billing currency at creation.
	Currency string `gorm:"size:8;not null"`
	// Version is the optimistic-lock counter (AD11).
	Version int64 `gorm:"not null;default:0"`
	// CreatedAt is the row write time (UTC).
	CreatedAt time.Time
	// UpdatedAt is bumped on every write.
	UpdatedAt time.Time
}

// TableName overrides the default GORM table name.
func (Account) TableName() string { return "accounts" }

// Transaction is one append-only ledger row (AD7): a recharge, a
// settlement deduction or a refund. Rows are never updated or deleted;
// the globally unique idempotency key is the replay guard.
type Transaction struct {
	// ID is the server-generated UUID v4, exposed as transaction_id.
	ID string `gorm:"primaryKey;type:uuid"`
	// AccountID is the owning account.
	AccountID string `gorm:"size:64;not null;index:idx_transactions_account_created,priority:1"`
	// Type is "recharge", "deduction" or "refund"; "adjustment" is
	// reserved.
	Type string `gorm:"size:16;not null"`
	// AmountCents is positive; the direction is implied by the type
	// (AD7).
	AmountCents int64 `gorm:"not null"`
	// BalanceAfterCents is the account's balance after the write.
	BalanceAfterCents int64 `gorm:"not null"`
	// IdempotencyKey is caller-supplied (recharge/refund) or
	// "charge:{charge_id}" (deduction); globally unique.
	IdempotencyKey string `gorm:"size:128;not null;uniqueIndex"`
	// Reference is the charge_id for deductions, free text otherwise.
	Reference string `gorm:"size:128;not null;default:''"`
	// CreatedAt is the write time (UTC).
	CreatedAt time.Time `gorm:"not null;index:idx_transactions_account_created,priority:2"`
}

// TableName overrides the default GORM table name.
func (Transaction) TableName() string { return "transactions" }
