package billing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Account mode and policy constants (AD1/AD4).
const (
	AccountModePrepaid  = "prepaid"
	AccountModePostpaid = "postpaid"

	OverdrawBlock = "block"
	OverdrawWarn  = "warn"

	TransactionTypeRecharge  = "recharge"
	TransactionTypeDeduction = "deduction"
	TransactionTypeRefund    = "refund"
)

// maxBalanceRetries bounds the optimistic-lock retry loop on balance
// writes (AD11).
const maxBalanceRetries = 8

// AccountRepository persists accounts and the transaction ledger. All
// writes are single-transaction and version-guarded; reads join an
// open transaction via the context.
type AccountRepository struct {
	db *gorm.DB
}

// NewAccountRepository constructs an AccountRepository bound to a
// gorm.DB.
func NewAccountRepository(db *gorm.DB) *AccountRepository {
	return &AccountRepository{db: db}
}

// DB resolves the gorm handle for the context, joining an open
// transaction when present (the database.Manager pattern).
func (r *AccountRepository) DB(ctx context.Context) *gorm.DB {
	return database.NewManager(r.db).DB(ctx)
}

// FindByOrg returns the organization's account, nil when absent
// (AC12).
func (r *AccountRepository) FindByOrg(ctx context.Context, orgID string) (*Account, error) {
	var row Account
	err := r.DB(ctx).Where("organization_id = ?", orgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "billing: account lookup failed")
	}
	return &row, nil
}

// FindByID returns one account by id, nil when absent.
func (r *AccountRepository) FindByID(ctx context.Context, id string) (*Account, error) {
	var row Account
	err := r.DB(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "billing: account lookup failed")
	}
	return &row, nil
}

// CreateAccount inserts the org's account; an organization_id unique
// violation maps to 10509 (one account per org, AD1). An optional
// opening balance is credited as a "recharge" transaction with the
// idempotency key "opening:{account_id}" in the same transaction.
func (r *AccountRepository) CreateAccount(ctx context.Context, account *Account, initialBalanceCents int64) (*Account, *Transaction, error) {
	if account.ID == "" {
		account.ID = uuid.NewString()
	}
	var opening *Transaction
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(account)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// The org already has an account (unique index).
			return apierrors.New(apierrors.CodeAccountInvalid)
		}
		if initialBalanceCents > 0 {
			opening = &Transaction{
				ID:                uuid.NewString(),
				AccountID:         account.ID,
				Type:              TransactionTypeRecharge,
				AmountCents:       initialBalanceCents,
				BalanceAfterCents: initialBalanceCents,
				IdempotencyKey:    "opening:" + account.ID,
				Reference:         "opening balance",
				CreatedAt:         time.Now().UTC(),
			}
			if err := tx.Create(opening).Error; err != nil {
				return err
			}
			if err := tx.Model(&Account{}).Where("id = ?", account.ID).
				Update("balance_cents", initialBalanceCents).Error; err != nil {
				return err
			}
			account.BalanceCents = initialBalanceCents
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return account, opening, nil
}

// UpdateAccount replaces the mode, quota and overdraw policy fields
// (AD12), version-guarded.
func (r *AccountRepository) UpdateAccount(ctx context.Context, account *Account, mode string, quotaCents int64, policy string) (*Account, error) {
	result := r.DB(ctx).Model(&Account{}).
		Where("id = ? AND version = ?", account.ID, account.Version).
		Updates(map[string]interface{}{
			"mode":                 mode,
			"monthly_quota_cents":  quotaCents,
			"overdraw_policy":      policy,
			"version":              account.Version + 1,
			"updated_at":           time.Now().UTC(),
		})
	if result.Error != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, result.Error, "billing: account update failed")
	}
	if result.RowsAffected == 0 {
		return nil, apierrors.New(apierrors.CodeAccountInvalid)
	}
	return r.FindByID(ctx, account.ID)
}

// Recharge credits the prepaid balance in one transaction: idempotency
// lookup (same account/type/amount converges, anything else is 10510),
// INSERT the ledger row, then the version-guarded balance UPDATE with
// bounded retry (AD11).
func (r *AccountRepository) Recharge(ctx context.Context, account *Account, amountCents int64, idempotencyKey, note, txType string) (*Account, *Transaction, error) {
	var out *Transaction
	var updated *Account
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := context.WithValue(ctx, database.TxKey{}, tx)
		// Idempotency lookup: a replayed key must converge.
		var existing Transaction
		err := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error
		if err == nil {
			if existing.AccountID != account.ID || existing.Type != txType || existing.AmountCents != amountCents {
				return apierrors.New(apierrors.CodeTransactionInvalid)
			}
			out = &existing
			updated, err = r.FindByID(txCtx, account.ID)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		row := &Transaction{
			ID:                uuid.NewString(),
			AccountID:         account.ID,
			Type:              txType,
			AmountCents:       amountCents,
			BalanceAfterCents: 0,
			IdempotencyKey:    idempotencyKey,
			Reference:         note,
			CreatedAt:         time.Now().UTC(),
		}
		// Bounded optimistic retry on the balance write.
		for attempt := 0; attempt < maxBalanceRetries; attempt++ {
			var current Account
			if err := tx.Where("id = ?", account.ID).First(&current).Error; err != nil {
				return err
			}
			delta := amountCents
			if txType == TransactionTypeRefund {
				delta = -amountCents
			}
			newBalance := current.BalanceCents + delta
			result := tx.Model(&Account{}).
				Where("id = ? AND version = ?", current.ID, current.Version).
				Updates(map[string]interface{}{
					"balance_cents": newBalance,
					"version":       current.Version + 1,
					"updated_at":    time.Now().UTC(),
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				row.BalanceAfterCents = newBalance
				if err := tx.Create(row).Error; err != nil {
					return err
				}
				out = row
				updated = &current
				updated.BalanceCents = newBalance
				updated.Version = current.Version + 1
				return nil
			}
			// Version conflict: retry with the fresh row.
		}
		return apierrors.Newf(apierrors.CodeInternal, "billing: balance write conflict exhausted retries")
	})
	if err != nil {
		return nil, nil, err
	}
	return updated, out, nil
}

// DeductionSpec describes one settlement deduction inside the charge
// transaction (AD3).
type DeductionSpec struct {
	AccountID   string
	ChargeID    string
	AmountCents int64
}

// ApplyDeduction joins the caller's open transaction: re-read the
// account, insert the deduction ledger row (key "charge:{charge_id}"),
// then the version-guarded balance/usage UPDATE. A replayed key
// converges without a second write (AC4).
func (r *AccountRepository) ApplyDeduction(ctx context.Context, spec DeductionSpec) error {
	tx, ok := ctx.Value(database.TxKey{}).(*gorm.DB)
	if !ok {
		// No open transaction: open one (defensive; the charge path
		// always supplies one).
		return r.db.WithContext(ctx).Transaction(func(inner *gorm.DB) error {
			return r.ApplyDeduction(context.WithValue(ctx, database.TxKey{}, inner), spec)
		})
	}
	// Idempotency: a replayed charge key converges (AC4).
	var existing Transaction
	err := tx.Where("idempotency_key = ?", "charge:"+spec.ChargeID).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var account Account
	if err := tx.Where("id = ?", spec.AccountID).First(&account).Error; err != nil {
		return err
	}
	// Prepaid deducts from balance; postpaid leaves the balance
	// untouched and only accrues cycle usage (AC5).
	newBalance := account.BalanceCents
	newUsed := account.UsedThisCycleCents
	if account.Mode == AccountModePostpaid {
		newUsed += spec.AmountCents
	} else {
		newBalance -= spec.AmountCents
	}
	row := &Transaction{
		ID:                uuid.NewString(),
		AccountID:         account.ID,
		Type:              TransactionTypeDeduction,
		AmountCents:       spec.AmountCents,
		BalanceAfterCents: newBalance,
		IdempotencyKey:    "charge:" + spec.ChargeID,
		Reference:         spec.ChargeID,
		CreatedAt:         time.Now().UTC(),
	}
	if err := tx.Create(row).Error; err != nil {
		return err
	}
	result := tx.Model(&Account{}).
		Where("id = ? AND version = ?", account.ID, account.Version).
		Updates(map[string]interface{}{
			"balance_cents":         newBalance,
			"used_this_cycle_cents": newUsed,
			"version":               account.Version + 1,
			"updated_at":            time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apierrors.Newf(apierrors.CodeInternal, "billing: deduction version conflict")
	}
	return nil
}

// TransactionFilter is the ListTransactions query (FR6).
type TransactionFilter struct {
	AccountID string
	Type      string
	Since     int64
	Until     int64
	Offset    int
	Limit     int
}

// ListTransactions returns one page of ledger rows, newest first, and
// the total count.
func (r *AccountRepository) ListTransactions(ctx context.Context, filter TransactionFilter) ([]*Transaction, int64, error) {
	query := r.DB(ctx).Model(&Transaction{})
	if filter.AccountID != "" {
		query = query.Where("account_id = ?", filter.AccountID)
	}
	if filter.Type != "" {
		query = query.Where("type = ?", filter.Type)
	}
	if filter.Since > 0 {
		query = query.Where("created_at >= ?", time.Unix(filter.Since, 0).UTC())
	}
	if filter.Until > 0 {
		// until is second-granularity and inclusive: rows carry
		// sub-second timestamps, so bound at the end of the second.
		query = query.Where("created_at < ?", time.Unix(filter.Until, 0).UTC().Add(time.Second))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// GORM maps Limit(0) to SQL LIMIT 0 (zero rows); default the
	// page size when the caller did not set one.
	limit := filter.Limit
	if limit <= 0 {
		limit = listDefaultLimit
	}
	var rows []*Transaction
	if err := query.
		Order("created_at DESC").Order("id DESC").
		Offset(filter.Offset).Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ResetCycle zeroes the postpaid usage of accounts whose cycle predates
// monthStart — the AD6 guarded UPDATE, idempotent by condition.
func (r *AccountRepository) ResetCycle(ctx context.Context, monthStart int64) (int64, error) {
	result := r.DB(ctx).Model(&Account{}).
		Where("mode = ? AND cycle_started_at < ?", AccountModePostpaid, monthStart).
		Updates(map[string]interface{}{
			"used_this_cycle_cents": 0,
			"cycle_started_at":      monthStart,
			"version":               gorm.Expr("version + 1"),
			"updated_at":            time.Now().UTC(),
		})
	if result.Error != nil {
		return 0, apierrors.Wrap(apierrors.CodeInternal, result.Error, "billing: cycle reset failed")
	}
	return result.RowsAffected, nil
}

// CheckFundsSnapshot returns the org's account for the gating check,
// nil when absent (AC12 — ungated).
func (r *AccountRepository) CheckFundsSnapshot(ctx context.Context, orgID string) (*Account, error) {
	return r.FindByOrg(ctx, orgID)
}
