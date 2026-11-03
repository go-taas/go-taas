package billing

import (
	"context"
	"strings"
	"time"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

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
func (s *Service) resolveAccountInOrg(ctx context.Context, repo *AccountRepository, orgID, accountID string) (*Account, error) {
	account, err := repo.FindByID(ctx, strings.TrimSpace(accountID))
	if err != nil {
		return nil, err
	}
	if account == nil || account.OrganizationID != orgID {
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
	return s.moneyTx(ctx, req.GetAccountId(), req.GetAmountCents(), req.GetIdempotencyKey(), req.GetNote(), TransactionTypeRecharge)
}

// Refund returns credit from a prepaid balance (D8).
func (s *Service) Refund(ctx context.Context, req *billingv1.RefundRequest) (*billingv1.RefundResponse, error) {
	resp, err := s.moneyTx(ctx, req.GetAccountId(), req.GetAmountCents(), req.GetIdempotencyKey(), req.GetNote(), TransactionTypeRefund)
	if err != nil {
		return nil, err
	}
	return &billingv1.RefundResponse{
		Response:    resp.Response,
		Account:     resp.Account,
		Transaction: resp.Transaction,
	}, nil
}

// moneyTx implements Recharge and Refund: validation, the idempotent
// ledger write, and the version-guarded balance update.
func (s *Service) moneyTx(ctx context.Context, accountID string, amountCents int64, idempotencyKey, note, txType string) (*billingv1.RechargeResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, true); err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(note)) > maxNoteLen {
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
	updated, tx, err := repo.Recharge(ctx, account, amountCents, strings.TrimSpace(idempotencyKey), strings.TrimSpace(note), txType)
	if err != nil {
		return nil, err
	}
	// Feature #15: record the successful recharge/refund best-effort.
	action := "billing.recharge"
	if txType == TransactionTypeRefund {
		action = "billing.refund"
	}
	s.recordAudit(ctx, &audit.AuditEvent{
		OrganizationID: orgID,
		ActorUserID:    orgID,
		ActorType:      "user",
		Action:         action,
		ResourceType:   "account",
		ResourceID:     accountID,
		Result:         "success",
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
		Response:           okResponse(),
		Mode:               account.Mode,
		Balance:            float64(account.BalanceCents) / 100,
		Currency:           account.Currency,
		BalanceCents:       account.BalanceCents,
		MonthlyQuotaCents:  account.MonthlyQuotaCents,
		UsedThisCycleCents: account.UsedThisCycleCents,
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
	return &billingv1.Transaction{
		TransactionId:     t.ID,
		AccountId:         t.AccountID,
		Type:              t.Type,
		AmountCents:       t.AmountCents,
		BalanceAfterCents: t.BalanceAfterCents,
		IdempotencyKey:    t.IdempotencyKey,
		Reference:         t.Reference,
		CreatedAt:         t.CreatedAt.Unix(),
	}
}
