package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/webhook"
)

// Account field limits (architecture Section 5.1).
const (
	maxIdempotencyKeyLen = 128
	maxNoteLen           = 128
)

// accountRepo resolves the account repository from the wired billing
// repository.
func (s *Service) accountRepo() (*AccountRepository, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	return repo.Accounts(), nil
}

// validateAccountFields checks the shared create/update matrix
// (Section 5.1): every failure is 10509, nothing written.
func validateAccountFields(mode string, quotaCents int64, policy string) error {
	if mode != AccountModePrepaid && mode != AccountModePostpaid {
		return apierrors.New(apierrors.CodeAccountInvalid)
	}
	if quotaCents < 0 {
		return apierrors.New(apierrors.CodeAccountInvalid)
	}
	if policy != OverdrawBlock && policy != OverdrawWarn {
		return apierrors.New(apierrors.CodeAccountInvalid)
	}
	return nil
}

// CreateAccount creates the org's single billing account; an optional
// opening balance is credited as a recharge transaction (AC1).
func (s *Service) CreateAccount(ctx context.Context, req *billingv1.CreateAccountRequest) (*billingv1.CreateAccountResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, true); err != nil {
		return nil, err
	}
	mode := strings.TrimSpace(req.GetMode())
	policy := strings.TrimSpace(req.GetOverdrawPolicy())
	if policy == "" {
		policy = OverdrawBlock
	}
	if err := validateAccountFields(mode, req.GetMonthlyQuotaCents(), policy); err != nil {
		return nil, err
	}
	if req.GetInitialBalanceCents() < 0 {
		return nil, apierrors.New(apierrors.CodeAccountInvalid)
	}
	if mode != AccountModePrepaid && req.GetInitialBalanceCents() > 0 {
		return nil, apierrors.New(apierrors.CodeAccountInvalid)
	}
	if req.GetMonthlySpendLimitCents() < 0 {
		return nil, apierrors.New(apierrors.CodeAccountInvalid)
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	account := &Account{
		OrganizationID:         orgID,
		Mode:                   mode,
		BalanceCents:           0,
		MonthlyQuotaCents:      req.GetMonthlyQuotaCents(),
		OverdrawPolicy:         policy,
		Currency:               config.GetConfig().Billing.Currency,
		CycleStartedAt:         monthStartOf(now),
		MonthlySpendLimitCents: req.GetMonthlySpendLimitCents(),
	}
	created, _, err := repo.CreateAccount(ctx, account, req.GetInitialBalanceCents())
	if err != nil {
		return nil, err
	}
	// Feature #15: record the successful account creation best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "billing.account.create",
		ResourceType:   "account",
		ResourceID:     created.ID,
		Result:         "success",
	})
	return &billingv1.CreateAccountResponse{Response: okResponse(), Account: summarizeAccount(created)}, nil
}

// ListAccounts returns the header org's accounts (0..1 rows, AC10).
func (s *Service) ListAccounts(ctx context.Context, req *billingv1.ListAccountsRequest) (*billingv1.ListAccountsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := repo.FindByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	accounts := make([]*billingv1.Account, 0, 1)
	if account != nil {
		accounts = append(accounts, summarizeAccount(account))
	}
	offset, limit := normalizePagination(req.GetPage())
	return &billingv1.ListAccountsResponse{
		Response: okResponse(),
		Accounts: accounts,
		PageMeta: &commonv1.PageMeta{Total: int64(len(accounts)), Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// resolveAccountInOrg loads the account by id and enforces org scoping
// (10503 on mismatch or absence, AC10).
//
// A platform administrator owns no tenant, so their session resolves to an
// empty organization — and an account-scoped read (the ledger of the tenant they
// are funding) would then match nothing. For them the account id is the scope:
// the row they loaded is the tenant they asked about, and a non-empty
// organization must still match it. An ordinary caller with no organization
// still gets 10503 rather than a cross-tenant read.
func (s *Service) resolveAccountInOrg(ctx context.Context, repo *AccountRepository, orgID, accountID string) (*Account, error) {
	account, err := repo.FindByID(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, apierrors.New(apierrors.CodeAccountNotFound)
	}
	if orgID != "" && account.OrganizationID != orgID {
		return nil, apierrors.New(apierrors.CodeAccountNotFound)
	}
	if orgID == "" && !authctx.IsPlatformAdmin(ctx) {
		return nil, apierrors.New(apierrors.CodeAccountNotFound)
	}
	return account, nil
}

// GetAccount returns one account with computed remaining funds and
// quota usage.
func (s *Service) GetAccount(ctx context.Context, req *billingv1.GetAccountRequest) (*billingv1.GetAccountResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := s.resolveAccountInOrg(ctx, repo, orgID, req.GetAccountId())
	if err != nil {
		return nil, err
	}
	return &billingv1.GetAccountResponse{Response: okResponse(), Account: summarizeAccount(account)}, nil
}

// UpdateAccount replaces mode, quota and overdraw policy (AD12);
// balance/usage fields are never settable here.
func (s *Service) UpdateAccount(ctx context.Context, req *billingv1.UpdateAccountRequest) (*billingv1.UpdateAccountResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, true); err != nil {
		return nil, err
	}
	mode := strings.TrimSpace(req.GetMode())
	if err := validateAccountFields(mode, req.GetMonthlyQuotaCents(), strings.TrimSpace(req.GetOverdrawPolicy())); err != nil {
		return nil, err
	}
	if req.GetMonthlySpendLimitCents() < 0 {
		return nil, apierrors.New(apierrors.CodeAccountInvalid)
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := s.resolveAccountInOrg(ctx, repo, orgID, req.GetAccountId())
	if err != nil {
		return nil, err
	}
	updated, err := repo.UpdateAccount(ctx, account, mode, req.GetMonthlyQuotaCents(), strings.TrimSpace(req.GetOverdrawPolicy()), req.GetMonthlySpendLimitCents())
	if err != nil {
		return nil, err
	}
	// Feature #15: record the successful account update best-effort.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         "billing.account.update",
		ResourceType:   "account",
		ResourceID:     updated.ID,
		Result:         "success",
	})
	return &billingv1.UpdateAccountResponse{Response: okResponse(), Account: summarizeAccount(updated)}, nil
}

// validateMoneyTx checks the recharge/refund matrix (Section 5.1):
// 10503 account, 10510 mode/amount/key.
func (s *Service) validateMoneyTx(ctx context.Context, repo *AccountRepository, orgID, accountID string, amountCents int64, idempotencyKey string, requirePrepaid bool) (*Account, error) {
	account, err := s.resolveAccountInOrg(ctx, repo, orgID, accountID)
	if err != nil {
		return nil, err
	}
	if requirePrepaid && account.Mode != AccountModePrepaid {
		return nil, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	if amountCents <= 0 {
		return nil, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	key := strings.TrimSpace(idempotencyKey)
	if key == "" || len(key) > maxIdempotencyKeyLen {
		return nil, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	return account, nil
}

// Recharge credits a prepaid balance, idempotent on the key (AC2/AC3).
func (s *Service) Recharge(ctx context.Context, req *billingv1.RechargeRequest) (*billingv1.RechargeResponse, error) {
	movement, err := s.operatorMovement(ctx, req.GetSource(), req.GetNote(), false)
	if err != nil {
		return nil, err
	}
	return s.moneyTx(ctx, req.GetAccountId(), req.GetAmountCents(), req.GetIdempotencyKey(), movement, TransactionTypeRecharge)
}

// Refund returns credit from a prepaid balance (D8).
func (s *Service) Refund(ctx context.Context, req *billingv1.RefundRequest) (*billingv1.RefundResponse, error) {
	movement, err := s.operatorMovement(ctx, req.GetSource(), req.GetNote(), false)
	if err != nil {
		return nil, err
	}
	resp, err := s.moneyTx(ctx, req.GetAccountId(), req.GetAmountCents(), req.GetIdempotencyKey(), movement, TransactionTypeRefund)
	if err != nil {
		return nil, err
	}
	return &billingv1.RefundResponse{
		Response:    resp.Response,
		Account:     resp.Account,
		Transaction: resp.Transaction,
	}, nil
}

// operatorMovement resolves the source, reason and acting administrator of a
// manual money movement.
//
// The source is what the ledger shows next to the amount, so it is validated
// rather than trusted: only the operator-set values are accepted from a
// caller, and an empty value defaults to a plain manual movement. The actor is
// the authenticated session user — never a field, so a caller cannot attribute
// its own action to somebody else.
func (s *Service) operatorMovement(ctx context.Context, source, note string, requireNote bool) (MoneyMovement, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = SourceAdminManual
	}
	if !OperatorSources[source] {
		return MoneyMovement{}, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	note = strings.TrimSpace(note)
	if requireNote && note == "" {
		return MoneyMovement{}, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	actor, _, err := s.resolveSessionActor(ctx)
	if err != nil {
		return MoneyMovement{}, err
	}
	return MoneyMovement{Note: note, Source: source, ActorUserID: actor}, nil
}

// moneyTx implements Recharge and Refund: validation, the idempotent
// ledger write, and the version-guarded balance update.
func (s *Service) moneyTx(ctx context.Context, accountID string, amountCents int64, idempotencyKey string, movement MoneyMovement, txType string) (*billingv1.RechargeResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, true); err != nil {
		return nil, err
	}
	if len(movement.Note) > maxNoteLen {
		return nil, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := s.validateMoneyTx(ctx, repo, orgID, accountID, amountCents, idempotencyKey, true)
	if err != nil {
		return nil, err
	}
	updated, tx, err := repo.Recharge(ctx, account, amountCents, strings.TrimSpace(idempotencyKey), movement, txType)
	if err != nil {
		return nil, err
	}
	// Feature #15: record the successful recharge/refund best-effort.
	action := "billing.recharge"
	if txType == TransactionTypeRefund {
		action = "billing.refund"
	}
	// The audit row names the acting administrator and the amount: a manual
	// credit is exactly the kind of action an operator is asked about later.
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    movement.ActorUserID,
		ActorType:      "user",
		Action:         action,
		ResourceType:   "account",
		ResourceID:     accountID,
		Result:         "success",
		Metadata: string(mustJSON(map[string]any{
			"amount_cents": amountCents,
			"source":       movement.Source,
			"note":         movement.Note,
		})),
	})
	// Feature #23 (AD10): publish the billing.balance_low webhook event
	// best-effort when the balance is below a low threshold (e.g. one
	// recharge unit). A publish failure is logged and never fails the
	// mutation.
	if updated.BalanceCents < 10000 {
		s.publishWebhookEvent(ctx, orgID, webhook.EventBalanceLow, "bal-"+accountID,
			map[string]any{
				"account_id":      accountID,
				"organization_id": orgID,
				"balance_cents":   updated.BalanceCents,
			})
		// Feature #26 (AD9): publish the same event to notification.events.
		s.publishNotificationEvent(ctx, orgID, webhook.EventBalanceLow, "bal-"+accountID,
			map[string]any{
				"account_id":      accountID,
				"organization_id": orgID,
				"balance_cents":   updated.BalanceCents,
			})
	}
	return &billingv1.RechargeResponse{
		Response:    okResponse(),
		Account:     summarizeAccount(updated),
		Transaction: summarizeTransaction(tx),
	}, nil
}

// ListTransactions returns the ledger filtered by account, type and
// time range, paginated newest-first (FR6, AC9).
func (s *Service) ListTransactions(ctx context.Context, req *billingv1.ListTransactionsRequest) (*billingv1.ListTransactionsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	since, until, err := validateBillingRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	// The account filter must resolve within the org (AC10).
	var accountID string
	if key := strings.TrimSpace(req.GetAccountId()); key != "" {
		account, err := s.resolveAccountInOrg(ctx, repo, orgID, key)
		if err != nil {
			return nil, err
		}
		accountID = account.ID
	}
	txType := strings.TrimSpace(req.GetType())
	switch txType {
	case "", TransactionTypeRecharge, TransactionTypeDeduction, TransactionTypeRefund:
	default:
		return nil, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListTransactions(ctx, TransactionFilter{
		AccountID: accountID,
		Type:      txType,
		Since:     since,
		Until:     until,
		Offset:    offset,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	txs := make([]*billingv1.Transaction, 0, len(rows))
	for _, row := range rows {
		txs = append(txs, summarizeTransaction(row))
	}
	return &billingv1.ListTransactionsResponse{
		Response:     okResponse(),
		Transactions: txs,
		PageMeta:     &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// CheckFunds is the internal gateway gating check (AD4): prepaid is
// allowed while balance > 0; postpaid is allowed under the quota
// unless the overdraw policy blocks; no account is ungated (AC12).
func (s *Service) CheckFunds(ctx context.Context, req *billingv1.CheckFundsRequest) (*billingv1.CheckFundsResponse, error) {
	orgID := strings.TrimSpace(req.GetOrganizationId())
	if orgID == "" {
		return nil, apierrors.New(apierrors.CodeUnauthorized)
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := repo.CheckFundsSnapshot(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return &billingv1.CheckFundsResponse{Response: okResponse(), Allowed: true, Mode: ""}, nil
	}
	allowed, reason := fundsAllowed(account)
	// Three-level spend gate: the project and personal caps tighten the
	// account cap (the minimum of the non-zero values). A resolver read
	// failure degrades that tier to unlimited and never blocks traffic:
	// the account rule above already ran.
	effectiveLimit := account.MonthlySpendLimitCents
	scopedSpent := account.SpentThisCycleCents
	ownerSpent := int64(0)
	projectSpent := int64(0)
	memberSpent := int64(0)
	if s.spendLimitResolver != nil {
		if ownerID := strings.TrimSpace(req.GetOwnerUserId()); ownerID != "" {
			limit, err := s.spendLimitResolver.UserMonthlySpendLimitCents(ctx, orgID, ownerID)
			if err != nil {
				logger.S().Warnw("billing: user spend limit read failed, degrading to unlimited",
					"org", orgID, "owner", ownerID, "err", err)
			} else {
				effectiveLimit = minNonZeroLimit(effectiveLimit, limit)
				ownerSpent, err = repo.ScopedSpentCents(ctx, orgID, account.CycleStartedAt, ownerID, "")
				if err != nil {
					logger.S().Warnw("billing: user spend read failed, degrading to unlimited",
						"org", orgID, "owner", ownerID, "err", err)
					ownerSpent = 0
				} else if limit > 0 && ownerSpent >= limit {
					allowed, reason = false, "insufficient_funds"
				}
				scopedSpent = ownerSpent
			}
		}
		if projectID := strings.TrimSpace(req.GetProjectId()); projectID != "" {
			limit, err := s.spendLimitResolver.ProjectMonthlySpendLimitCents(ctx, projectID)
			if err != nil {
				logger.S().Warnw("billing: project spend limit read failed, degrading to unlimited",
					"org", orgID, "project", projectID, "err", err)
			} else {
				effectiveLimit = minNonZeroLimit(effectiveLimit, limit)
				projectSpent, err = repo.ScopedSpentCents(ctx, orgID, account.CycleStartedAt, "", projectID)
				if err != nil {
					logger.S().Warnw("billing: project spend read failed, degrading to unlimited",
						"org", orgID, "project", projectID, "err", err)
					projectSpent = 0
				} else if limit > 0 && projectSpent >= limit {
					allowed, reason = false, "insufficient_funds"
				}
				if strings.TrimSpace(req.GetOwnerUserId()) == "" {
					scopedSpent = projectSpent
				}
			}
		}
		// Member tier: the project administrator's cap on this person inside
		// this project. It is measured against the same person's spend in the
		// project, so a generous teammate never eats into this member's cap,
		// and the effective limit takes the tighter of it and the other tiers.
		if ownerID, projectID := strings.TrimSpace(req.GetOwnerUserId()), strings.TrimSpace(req.GetProjectId()); ownerID != "" && projectID != "" {
			limit, limitErr := s.spendLimitResolver.ProjectMemberMonthlySpendLimitCents(ctx, projectID, ownerID)
			if limitErr != nil {
				logger.S().Warnw("billing: project member spend limit read failed, degrading to unlimited",
					"org", orgID, "project", projectID, "owner", ownerID, "err", limitErr)
			} else {
				effectiveLimit = minNonZeroLimit(effectiveLimit, limit)
				memberSpent, err = repo.ScopedSpentCents(ctx, orgID, account.CycleStartedAt, ownerID, projectID)
				if err != nil {
					logger.S().Warnw("billing: project member spend read failed, degrading to unlimited",
						"org", orgID, "project", projectID, "owner", ownerID, "err", err)
					memberSpent = 0
				} else if limit > 0 && memberSpent >= limit {
					allowed, reason = false, "insufficient_funds"
				}
			}
		}
	}
	// Feature #26 (AD9): publish the billing.spend_limit_breached
	// notification event best-effort when the cross-mode spend limit is
	// reached. A publish failure is logged and never fails the check.
	if !allowed && account.MonthlySpendLimitCents > 0 && account.SpentThisCycleCents >= account.MonthlySpendLimitCents {
		s.publishNotificationEvent(ctx, orgID, webhook.EventSpendLimitBreached, "spend-"+account.ID,
			map[string]any{
				"account_id":      account.ID,
				"organization_id": orgID,
				"spent_cents":     account.SpentThisCycleCents,
				"limit_cents":     account.MonthlySpendLimitCents,
			})
	}
	return &billingv1.CheckFundsResponse{
		Response:               okResponse(),
		Allowed:                allowed,
		Reason:                 reason,
		Mode:                   account.Mode,
		BalanceCents:           account.BalanceCents,
		MonthlyQuotaCents:      account.MonthlyQuotaCents,
		UsedThisCycleCents:     account.UsedThisCycleCents,
		MonthlySpendLimitCents: account.MonthlySpendLimitCents,
		SpentThisCycleCents:    account.SpentThisCycleCents,

		EffectiveMonthlySpendLimitCents:  effectiveLimit,
		OwnerSpentThisCycleCents:         ownerSpent,
		ProjectSpentThisCycleCents:       projectSpent,
		ScopedSpentThisCycleCents:        scopedSpent,
		ProjectMemberSpentThisCycleCents: memberSpent,
	}, nil
}

// minNonZeroLimit returns the smaller of two limits, treating 0 as
// unlimited rather than as the smallest value.
func minNonZeroLimit(a, b int64) int64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

// fundsAllowed evaluates the gating truth table (AC6/AC7): prepaid
// blocked at balance <= 0; postpaid blocked at quota exceeded only
// under the "block" policy; the cross-mode spend limit blocks when
// reached (feature #11, AD5/AD7).
func fundsAllowed(a *Account) (bool, string) {
	if a.MonthlySpendLimitCents > 0 && a.SpentThisCycleCents >= a.MonthlySpendLimitCents {
		return false, "insufficient_funds"
	}
	if a.Mode == AccountModePrepaid {
		if a.BalanceCents <= 0 {
			return false, "insufficient_funds"
		}
		return true, ""
	}
	if a.MonthlyQuotaCents > 0 && a.UsedThisCycleCents >= a.MonthlyQuotaCents {
		if a.OverdrawPolicy == OverdrawBlock {
			return false, "insufficient_funds"
		}
	}
	return true, ""
}

// GetBalance returns the org's account snapshot; the legacy double
// field stays one release for console compatibility (Section 5).
func (s *Service) GetBalance(ctx context.Context, _ *billingv1.GetBalanceRequest) (*billingv1.GetBalanceResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := repo.FindByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, apierrors.New(apierrors.CodeAccountNotFound)
	}
	return &billingv1.GetBalanceResponse{
		Response:            okResponse(),
		Mode:                account.Mode,
		Balance:             float64(account.BalanceCents) / 100,
		Currency:            account.Currency,
		BalanceCents:        account.BalanceCents,
		MonthlyQuotaCents:   account.MonthlyQuotaCents,
		UsedThisCycleCents:  account.UsedThisCycleCents,
		CycleStartedAt:      account.CycleStartedAt,
		SpentThisCycleCents: account.SpentThisCycleCents,
		HasAccount:          true,
	}, nil
}

// summarizeAccount maps an account row to the proto message with the
// computed remaining/usage fields.
func summarizeAccount(a *Account) *billingv1.Account {
	remaining := a.BalanceCents
	usage := int32(0)
	if a.Mode == AccountModePostpaid {
		if a.MonthlyQuotaCents > 0 {
			remaining = a.MonthlyQuotaCents - a.UsedThisCycleCents
			if remaining < 0 {
				remaining = 0
			}
			usage = clampInt32(int(a.UsedThisCycleCents * 100 / a.MonthlyQuotaCents))
		} else {
			remaining = 0
		}
	}
	spendUsage := int32(0)
	if a.MonthlySpendLimitCents > 0 {
		spendUsage = clampInt32(int(a.SpentThisCycleCents * 100 / a.MonthlySpendLimitCents))
	}
	return &billingv1.Account{
		AccountId:              a.ID,
		OrganizationId:         a.OrganizationID,
		Mode:                   a.Mode,
		BalanceCents:           a.BalanceCents,
		MonthlyQuotaCents:      a.MonthlyQuotaCents,
		UsedThisCycleCents:     a.UsedThisCycleCents,
		CycleStartedAt:         a.CycleStartedAt,
		OverdrawPolicy:         a.OverdrawPolicy,
		Currency:               a.Currency,
		RemainingCents:         remaining,
		QuotaUsagePercent:      usage,
		CreatedAt:              a.CreatedAt.Unix(),
		UpdatedAt:              a.UpdatedAt.Unix(),
		MonthlySpendLimitCents: a.MonthlySpendLimitCents,
		SpentThisCycleCents:    a.SpentThisCycleCents,
		SpendLimitUsagePercent: spendUsage,
	}
}

// summarizeTransaction maps a ledger row to the proto message.
func summarizeTransaction(t *Transaction) *billingv1.Transaction {
	out := &billingv1.Transaction{
		TransactionId:     t.ID,
		AccountId:         t.AccountID,
		Type:              t.Type,
		AmountCents:       t.AmountCents,
		BalanceAfterCents: t.BalanceAfterCents,
		IdempotencyKey:    t.IdempotencyKey,
		Reference:         t.Reference,
		Source:            t.Source,
		CreatedAt:         t.CreatedAt.Unix(),
	}
	if t.ActorUserID != nil {
		out.ActorUserId = *t.ActorUserID
	}
	return out
}

// mustJSON marshals v, returning a JSON null on the (impossible) error. It is
// used for the audit metadata, which is diagnostic: a marshal failure must not
// fail the money movement it describes.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// ---- Organization balances (platform administrator) ----

// organizationRow is the read-only projection of the tenancy module's
// organizations table: the billing module needs a tenant's name and state to
// present a balance, and importing tenancy for two columns would invert the
// dependency. The same pattern reads charge_records from metering (AD1).
type organizationRow struct {
	ID          string
	DisplayName string
	State       string
}

// TableName overrides the default GORM table name to tenancy's table.
func (organizationRow) TableName() string { return "organizations" }

// organizations returns every tenant, ordered by id for a stable listing.
func (s *Service) organizations(ctx context.Context) ([]organizationRow, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	var rows []organizationRow
	if err := repo.DB(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "billing: organization lookup failed")
	}
	return rows, nil
}

// findOrganization returns one tenant, or CodeOrganizationNotFound.
func (s *Service) findOrganization(ctx context.Context, orgID string) (*organizationRow, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	var row organizationRow
	err = repo.DB(ctx).Where("id = ?", orgID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "billing: organization lookup failed")
	}
	return &row, nil
}

// requirePlatformAdmin gates the fleet-wide money operations. A tenant
// administrator must never see or fund another tenant, and the fleet view has
// no organization to scope by, so the role itself is the boundary.
func requirePlatformAdmin(ctx context.Context) error {
	if authctx.IsPlatformAdmin(ctx) {
		return nil
	}
	return apierrors.Newf(apierrors.CodeForbidden,
		"billing: only a platform administrator can manage organization balances")
}

// ListOrganizationBalances returns every organization with its money position.
//
// Organizations without an account are included with has_account=false: the
// platform administrator's job here is to fund tenants, and an unfunded tenant
// has no account yet — hiding it would force a two-step "create, then recharge"
// ritual that the recharge endpoint already collapses into one.
func (s *Service) ListOrganizationBalances(ctx context.Context, req *billingv1.ListOrganizationBalancesRequest) (*billingv1.ListOrganizationBalancesResponse, error) {
	if err := requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	orgs, err := s.organizations(ctx)
	if err != nil {
		return nil, err
	}
	accRepo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	accounts, err := accRepo.ListAllAccounts(ctx)
	if err != nil {
		return nil, err
	}
	byOrg := make(map[string]*Account, len(accounts))
	for _, a := range accounts {
		byOrg[a.OrganizationID] = a
	}

	currency := config.GetConfig().Billing.Currency
	mode := strings.TrimSpace(req.GetMode())
	query := strings.ToLower(strings.TrimSpace(req.GetQuery()))

	rows := make([]*billingv1.OrganizationBalance, 0, len(orgs))
	for _, org := range orgs {
		b := summarizeOrganizationBalance(org, byOrg[org.ID], currency)
		if mode != "" && b.GetMode() != mode {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(org.DisplayName), query) &&
			!strings.Contains(strings.ToLower(org.ID), query) {
			continue
		}
		rows = append(rows, b)
	}

	offset, limit := normalizePagination(req.GetPage())
	total := int64(len(rows))
	var paged []*billingv1.OrganizationBalance
	if offset >= len(rows) {
		paged = nil
	} else {
		end := offset + limit
		if end > len(rows) || limit <= 0 {
			end = len(rows)
		}
		paged = rows[offset:end]
	}
	return &billingv1.ListOrganizationBalancesResponse{
		Response: okResponse(),
		Balances: paged,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
		Currency: currency,
	}, nil
}

// summarizeOrganizationBalance projects one tenant's money position. A tenant
// without an account reports zeros and no mode, which the console renders as
// "no account yet" next to the recharge action.
func summarizeOrganizationBalance(org organizationRow, account *Account, currency string) *billingv1.OrganizationBalance {
	out := &billingv1.OrganizationBalance{
		OrganizationId:    org.ID,
		OrganizationName:  org.DisplayName,
		OrganizationState: org.State,
		Currency:          currency,
	}
	if account == nil {
		return out
	}
	out.HasAccount = true
	out.AccountId = account.ID
	out.Mode = account.Mode
	out.BalanceCents = account.BalanceCents
	out.MonthlyQuotaCents = account.MonthlyQuotaCents
	out.UsedThisCycleCents = account.UsedThisCycleCents
	out.MonthlySpendLimitCents = account.MonthlySpendLimitCents
	out.SpentThisCycleCents = account.SpentThisCycleCents
	out.CycleStartedAt = account.CycleStartedAt
	out.Currency = account.Currency
	out.RemainingCents = remainingCents(account)
	return out
}

// remainingCents is the spendable amount now: the prepaid balance, or the
// postpaid quota minus its usage (0 when the quota is unlimited). A prepaid
// balance may be negative through settlement lag, and that is what it reports.
func remainingCents(a *Account) int64 {
	if a.Mode == AccountModePostpaid {
		if a.MonthlyQuotaCents <= 0 {
			return 0
		}
		remaining := a.MonthlyQuotaCents - a.UsedThisCycleCents
		if remaining < 0 {
			return 0
		}
		return remaining
	}
	return a.BalanceCents
}

// RechargeOrganization credits a tenant's prepaid balance, creating the account
// when it has none.
//
// This is the platform administrator's funding action, and every part of it is
// auditable: the transaction carries the source the administrator chose and the
// administrator's own id, and an audit event records the amount, source and
// reason. An organization that already runs postpaid is credited in place —
// switching modes is a separate decision the account editor owns, and silently
// flipping it here would change how the tenant is billed.
func (s *Service) RechargeOrganization(ctx context.Context, req *billingv1.RechargeOrganizationRequest) (*billingv1.RechargeOrganizationResponse, error) {
	return s.organizationMoneyTx(ctx, req.GetOrganizationId(), req.GetAmountCents(),
		req.GetIdempotencyKey(), req.GetNote(), req.GetSource(), TransactionTypeRecharge)
}

// DebitOrganization takes credit back from a tenant's prepaid balance. It is a
// correction, not a refund to the tenant, so a reason is required and the
// amount may not exceed the balance: a negative balance is a settlement-lag
// state, never something an operator creates on purpose.
func (s *Service) DebitOrganization(ctx context.Context, req *billingv1.DebitOrganizationRequest) (*billingv1.DebitOrganizationResponse, error) {
	resp, err := s.organizationMoneyTx(ctx, req.GetOrganizationId(), req.GetAmountCents(),
		req.GetIdempotencyKey(), req.GetNote(), req.GetSource(), TransactionTypeRefund)
	if err != nil {
		return nil, err
	}
	return &billingv1.DebitOrganizationResponse{
		Response:    resp.Response,
		Balance:     resp.Balance,
		Transaction: resp.Transaction,
	}, nil
}

// organizationMoneyTx implements RechargeOrganization and DebitOrganization:
// validation, the account (created on first credit), the idempotent ledger
// write, the audit event, and the projected balance.
func (s *Service) organizationMoneyTx(ctx context.Context, orgID string, amountCents int64, idempotencyKey, note, source, txType string) (*billingv1.RechargeOrganizationResponse, error) {
	if err := requirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, apierrors.New(apierrors.CodeOrganizationNotFound)
	}
	if amountCents <= 0 {
		return nil, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > maxIdempotencyKeyLen {
		return nil, apierrors.New(apierrors.CodeTransactionInvalid)
	}
	// A debit is a correction: it must say why.
	movement, err := s.operatorMovement(ctx, source, note, txType == TransactionTypeRefund)
	if err != nil {
		return nil, err
	}

	org, err := s.findOrganization(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if org.State != "active" {
		return nil, apierrors.Newf(apierrors.CodeOrganizationDisabled,
			"billing: organization %s is %s; it cannot be funded", orgID, org.State)
	}

	accRepo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := accRepo.FindByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	created := false
	if account == nil {
		if txType == TransactionTypeRefund {
			// Nothing to take back from: an organization without an account
			// has no balance.
			return nil, apierrors.New(apierrors.CodeAccountNotFound)
		}
		currency := config.GetConfig().Billing.Currency
		account = &Account{
			ID:             uuid.NewString(),
			OrganizationID: orgID,
			Mode:           AccountModePrepaid,
			Currency:       currency,
			CycleStartedAt: monthStartOf(time.Now().UTC()),
			OverdrawPolicy: OverdrawBlock,
		}
		created = true
		stored, _, createErr := accRepo.CreateAccount(ctx, account, 0)
		if createErr != nil {
			return nil, createErr
		}
		account = stored
	}
	if txType == TransactionTypeRefund && account.Mode == AccountModePrepaid && amountCents > account.BalanceCents {
		return nil, apierrors.New(apierrors.CodeInsufficientFunds)
	}

	updated, tx, err := accRepo.Recharge(ctx, account, amountCents, idempotencyKey, movement, txType)
	if err != nil {
		return nil, err
	}

	action := "billing.organization_recharge"
	if txType == TransactionTypeRefund {
		action = "billing.organization_debit"
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    movement.ActorUserID,
		ActorType:      "user",
		Action:         action,
		ResourceType:   "account",
		ResourceID:     account.ID,
		Result:         "success",
		Metadata: string(mustJSON(map[string]any{
			"amount_cents":     amountCents,
			"source":           movement.Source,
			"note":             movement.Note,
			"account_created":  created,
			"balance_after":    updated.BalanceCents,
			"organization_id":  orgID,
			"transaction_type": txType,
		})),
	})

	return &billingv1.RechargeOrganizationResponse{
		Response:       okResponse(),
		Balance:        summarizeOrganizationBalance(*org, updated, updated.Currency),
		Transaction:    s.summarizeTransactionWithActor(ctx, tx),
		AccountCreated: created,
	}, nil
}

// summarizeTransactionWithActor projects a ledger row and resolves the acting
// administrator's display name, so the console can show a person instead of a
// UUID without a second round trip.
func (s *Service) summarizeTransactionWithActor(ctx context.Context, t *Transaction) *billingv1.Transaction {
	out := summarizeTransaction(t)
	if out.GetActorUserId() == "" || s.userLabelResolver == nil {
		return out
	}
	labels, err := s.userLabelResolver.ResolveUserLabels(ctx, []string{out.GetActorUserId()})
	if err != nil {
		return out
	}
	out.ActorDisplayName = labels[out.GetActorUserId()]
	return out
}

// SpentCents implements the tenancy module's SpendReader seam: the settled
// charge amount of one scope within the current UTC cycle, which is the window
// the spend gate uses.
//
// It reads the cross-mode cycle total for the whole organization, and the
// charge records for a narrower scope. Charge records carry the project and the
// owner at settlement time (an attribution snapshot), so a project or member
// figure never shifts when a key is later transferred.
func (s *Service) SpentCents(ctx context.Context, orgID, projectID, ownerUserID string) (int64, error) {
	if strings.TrimSpace(orgID) == "" {
		return 0, nil
	}
	repo, err := s.accountRepo()
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(projectID) == "" && strings.TrimSpace(ownerUserID) == "" {
		account, findErr := repo.FindByOrg(ctx, orgID)
		if findErr != nil {
			return 0, findErr
		}
		if account == nil {
			return 0, nil
		}
		return account.SpentThisCycleCents, nil
	}
	cycleStart := monthStartOf(time.Now().UTC())
	return repo.ScopedSpentCents(ctx, orgID, cycleStart, ownerUserID, projectID)
}
