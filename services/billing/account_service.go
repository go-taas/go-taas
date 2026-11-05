package billing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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
	// One label lookup for the whole page: a ledger names the administrators
	// who moved the money, and a per-row resolution would multiply the query
	// count by the page size.
	actors := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ActorUserID != nil && *row.ActorUserID != "" {
			actors = append(actors, *row.ActorUserID)
		}
	}
	labels := s.userLabels(ctx, actors)
	txs := make([]*billingv1.Transaction, 0, len(rows))
	for _, row := range rows {
		tx := summarizeTransaction(row)
		tx.ActorDisplayName = labels[tx.GetActorUserId()]
		txs = append(txs, tx)
	}
	return &billingv1.ListTransactionsResponse{
		Response:     okResponse(),
		Transactions: txs,
		PageMeta:     &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// FundsQuery is one admission decision's input.
//
// It is exported so the data plane's gating seam can be described without
// this package's protobuf types, and so a caller can say *which* request is
// being admitted: the reservation that closes the concurrency race is keyed
// by it.
type FundsQuery struct {
	// OrganizationID is the tenant whose money is being spent. An empty
	// organization is ungated: a platform-level caller owns no account.
	OrganizationID string
	// OwnerUserID and ProjectID select the spend-cap tiers that apply
	// (the personal, project and project-member caps).
	OwnerUserID string
	ProjectID   string
	// RequestID identifies the admission. When set and reservation is
	// enabled, the request holds its worst-case cost until metering
	// records it, so a concurrent burst cannot spend the same balance
	// twice.
	RequestID string
	// Model and MaxOutputTokens size that reservation: the output is the
	// only part of a request whose size is known before it runs.
	Model           string
	MaxOutputTokens int64
}

// fundsEvaluation is what one gate evaluation found.
type fundsEvaluation struct {
	account *Account
	allowed bool
	reason  string

	// accruedMicros is recorded but uncharged spend of the organization
	// scope (the open hour). The other scopes' figures are folded into
	// bounds as they are read.
	accruedMicros int64
	// holdsMicros is spend reserved by requests in flight, counted only when
	// the caller asked for it: an admission leaves holds to the reserve
	// script, which reads them itself.
	holdsMicros int64

	effectiveLimit int64
	ownerSpent     int64
	projectSpent   int64
	scopedSpent    int64
	memberSpent    int64

	// bounds are the ceilings an admission must fit, one per scope that
	// constrains this request, with the recorded accrual already netted out.
	// Admission holds against all of them at once; a request denied by any one
	// of them leaves no hold behind.
	bounds []FundsBound
}

// scopeBound builds one scope's ceiling, or reports that the scope is
// unlimited and therefore constrains nothing.
func scopeBound(scope string, limitCents, spentCents, accruedMicros int64) (FundsBound, bool) {
	if limitCents <= 0 {
		return FundsBound{}, false
	}
	return FundsBound{
		Scope:           scope,
		AvailableMicros: CentsToMicros(limitCents-spentCents) - accruedMicros,
	}, true
}

// CheckInferenceAllowed answers the data plane's per-request funds gate.
//
// It is the primitive-argument face of CheckFunds so the auth module can
// depend on a narrow seam instead of this package's protobuf types. An
// empty organization is ungated (nothing to charge); a missing account is
// ungated too, matching the transitional open mode (AC12).
//
// When the request names itself and reservation is enabled, the answer also
// reserves the request's worst-case cost in every scope that constrains it:
// the evaluation is a read, and two concurrent reads of the same balance would
// both say yes.
func (s *Service) CheckInferenceAllowed(ctx context.Context, orgID, ownerUserID, projectID, requestID, model string, maxOutputTokens int64) (bool, string, error) {
	return s.evaluateAdmission(ctx, FundsQuery{
		OrganizationID:  orgID,
		OwnerUserID:     ownerUserID,
		ProjectID:       projectID,
		RequestID:       requestID,
		Model:           model,
		MaxOutputTokens: maxOutputTokens,
	})
}

// ReservationActive reports whether admissions reserve their cost. The data
// plane needs to know: a reserved verdict is per-request and must not be
// cached, or the next request would be admitted without a reservation.
func (s *Service) ReservationActive() bool { return s.fundsReservationEnabled() }

// evaluateAdmission is the struct-shaped core of CheckInferenceAllowed.
func (s *Service) evaluateAdmission(ctx context.Context, q FundsQuery) (bool, string, error) {
	// Nothing to charge: a platform-level caller owns no tenant account.
	if strings.TrimSpace(q.OrganizationID) == "" {
		return true, "", nil
	}
	eval, err := s.evaluateFunds(ctx, q, false)
	if err != nil {
		return false, "", err
	}
	if !eval.allowed {
		return false, eval.reason, nil
	}
	// No account means nothing to hold against (AC12), and a request with no
	// known size holds nothing either.
	if eval.account == nil || strings.TrimSpace(q.RequestID) == "" || !s.fundsReservationEnabled() {
		return true, "", nil
	}
	need := s.reserveMicros(ctx, q.Model, q.MaxOutputTokens)
	if need <= 0 {
		return true, "", nil
	}
	bounds := eval.reservableBounds()
	if len(bounds) == 0 {
		// The account rule admitted this request with nothing left — the
		// postpaid "warn" policy — so there is no ceiling to hold against and
		// holding would silently turn the policy into a block.
		return true, "", nil
	}
	admitted, err := s.reserver().Reserve(ctx, q.OrganizationID, q.RequestID, need, bounds)
	if err != nil {
		// Reservation is an optimisation over the accrual gate: a Redis
		// failure degrades to "checked, not held" instead of stopping
		// inference for a cache outage.
		logger.S().Warnw("billing: funds reservation failed, admitting on accrual only",
			"org", q.OrganizationID, "request_id", q.RequestID, "err", err)
		return true, "", nil
	}
	if !admitted {
		return false, "insufficient_funds", nil
	}
	return true, "", nil
}

// reservableBounds keeps the scopes a hold can still be taken against. A
// scope whose ceiling is already consumed cannot be reserved into — the
// account rule either denied the request or the policy is "warn" — and
// passing it to the script would turn that policy into a block.
func (e *fundsEvaluation) reservableBounds() []FundsBound {
	bounds := make([]FundsBound, 0, len(e.bounds))
	for _, b := range e.bounds {
		if b.AvailableMicros > 0 {
			bounds = append(bounds, b)
		}
	}
	return bounds
}

// reserveMicros sizes one request's hold: its output at the highest rate the
// model can be billed at, since neither the serving card nor the prompt size
// is known before the request runs. An unpriced model reserves nothing — the
// accrual gate still accounts the usage once metering records it.
func (s *Service) reserveMicros(ctx context.Context, model string, maxOutputTokens int64) int64 {
	model = strings.TrimSpace(model)
	if model == "" {
		return 0
	}
	tokens := maxOutputTokens
	if tokens <= 0 {
		tokens = config.GetConfig().Billing.Funds.DefaultOutputTokens
	}
	if tokens <= 0 {
		return 0
	}
	repo, err := s.repository()
	if err != nil {
		return 0
	}
	now := time.Now().Unix()
	entry, err := s.resolvePriceEntry(ctx, repo, model, defaultCardType, now)
	if err != nil {
		return 0
	}
	if entry == nil {
		entry, err = repo.WorstCasePrice(ctx, model, now)
		if err != nil {
			return 0
		}
	}
	return EstimateAccrualMicros(entry, 0, tokens, 0)
}

// outstandingHolds returns the micros reserved by in-flight requests in one
// scope, or 0 when reservation is off or Redis cannot answer.
func (s *Service) outstandingHolds(ctx context.Context, orgID, scope string) int64 {
	if !s.fundsReservationEnabled() {
		return 0
	}
	micros, err := s.reserver().Outstanding(ctx, orgID, scope)
	if err != nil {
		logger.S().Warnw("billing: funds hold read failed, gating on accrual only",
			"org", orgID, "scope", scope, "err", err)
		return 0
	}
	return micros
}

// releaseHold frees the reservation of one admitted request once metering
// recorded it: the line's own accrual takes over from there. Releasing a
// request that never held anything (a redelivery, or a request admitted
// before reservation existed) is a no-op.
func (s *Service) releaseHold(ctx context.Context, orgID, requestID string) {
	if strings.TrimSpace(orgID) == "" || strings.TrimSpace(requestID) == "" || !s.fundsReservationEnabled() {
		return
	}
	freed, err := s.reserver().Release(ctx, orgID, requestID)
	if err != nil {
		logger.S().Warnw("billing: funds hold release failed, the hold will expire",
			"org", orgID, "request_id", requestID, "err", err)
		return
	}
	if freed > 0 {
		logger.S().Debugw("billing: released funds hold",
			"org", orgID, "request_id", requestID, "micros", freed)
	}
}

// CheckFunds is the internal gateway gating check (AD4): prepaid is
// allowed while the balance covers the recorded and reserved spend; postpaid
// is allowed under the quota unless the overdraw policy blocks; no account is
// ungated (AC12). It never reserves: the console and legacy callers want the
// current position, not an admission.
func (s *Service) CheckFunds(ctx context.Context, req *billingv1.CheckFundsRequest) (*billingv1.CheckFundsResponse, error) {
	orgID := strings.TrimSpace(req.GetOrganizationId())
	if orgID == "" {
		return nil, apierrors.New(apierrors.CodeUnauthorized)
	}
	eval, err := s.evaluateFunds(ctx, FundsQuery{
		OrganizationID: orgID,
		OwnerUserID:    strings.TrimSpace(req.GetOwnerUserId()),
		ProjectID:      strings.TrimSpace(req.GetProjectId()),
	}, true)
	if err != nil {
		return nil, err
	}
	if eval.account == nil {
		return &billingv1.CheckFundsResponse{
			Response: okResponse(),
			Allowed:  eval.allowed,
			Reason:   eval.reason,
		}, nil
	}
	return &billingv1.CheckFundsResponse{
		Response:               okResponse(),
		Allowed:                eval.allowed,
		Reason:                 eval.reason,
		Mode:                   eval.account.Mode,
		BalanceCents:           eval.account.BalanceCents,
		MonthlyQuotaCents:      eval.account.MonthlyQuotaCents,
		UsedThisCycleCents:     eval.account.UsedThisCycleCents,
		MonthlySpendLimitCents: eval.account.MonthlySpendLimitCents,
		SpentThisCycleCents:    eval.account.SpentThisCycleCents,

		EffectiveMonthlySpendLimitCents:  eval.effectiveLimit,
		OwnerSpentThisCycleCents:         eval.ownerSpent,
		ProjectSpentThisCycleCents:       eval.projectSpent,
		ScopedSpentThisCycleCents:        eval.scopedSpent,
		ProjectMemberSpentThisCycleCents: eval.memberSpent,
	}, nil
}

// evaluateFunds runs the gating truth table: the account rule first, then the
// three-level spend caps that tighten it, and collects the ceiling of every
// scope so an admission can hold against all of them. A resolver read failure
// degrades that tier to unlimited and never blocks traffic.
//
// includeHolds is false on the admission path — the reserve script counts the
// holds atomically, and counting them here as well would refuse requests the
// tenant can still afford — and true for a read-only position report.
func (s *Service) evaluateFunds(ctx context.Context, q FundsQuery, includeHolds bool) (*fundsEvaluation, error) {
	orgID := strings.TrimSpace(q.OrganizationID)
	repo, err := s.accountRepo()
	if err != nil {
		return nil, err
	}
	account, err := repo.CheckFundsSnapshot(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		// The transitional open mode: an onboarded tenant is ungated until
		// someone opens its account. An operator who has finished onboarding
		// every tenant can close that door (AC12).
		if config.GetConfig().Billing.Funds.EnforceDefault {
			return &fundsEvaluation{account: nil, reason: reasonNoBillingAccount}, nil
		}
		return &fundsEvaluation{account: nil, allowed: true}, nil
	}

	// In-flight spend: usage written but not charged yet. Counting it is what
	// makes the open hour cost real money instead of becoming a free — and
	// later negative — hour.
	eval := &fundsEvaluation{account: account}
	eval.accruedMicros = s.scopedAccrual(ctx, repo, orgID, account.CycleStartedAt, "", "")
	if includeHolds {
		eval.holdsMicros = s.outstandingHolds(ctx, orgID, scopeOrganization)
	}
	inFlight := eval.accruedMicros + eval.holdsMicros
	eval.allowed, eval.reason = fundsAllowed(account, inFlight)

	// The account's own ceiling always constrains an admission: it is the
	// balance (prepaid) or the quota headroom (postpaid), tightened by the
	// cross-mode spend limit.
	eval.bounds = append(eval.bounds, FundsBound{
		Scope:           scopeOrganization,
		AvailableMicros: accountCeilingMicros(account) - eval.accruedMicros - eval.holdsMicros,
	})

	eval.effectiveLimit = account.MonthlySpendLimitCents
	eval.scopedSpent = account.SpentThisCycleCents
	if s.spendLimitResolver == nil {
		return eval, nil
	}
	ownerID := strings.TrimSpace(q.OwnerUserID)
	projectID := strings.TrimSpace(q.ProjectID)
	if ownerID != "" {
		limit, err := s.spendLimitResolver.UserMonthlySpendLimitCents(ctx, orgID, ownerID)
		if err != nil {
			logger.S().Warnw("billing: user spend limit read failed, degrading to unlimited",
				"org", orgID, "owner", ownerID, "err", err)
		} else {
			eval.effectiveLimit = minNonZeroLimit(eval.effectiveLimit, limit)
			spent, spentErr := repo.ScopedSpentCents(ctx, orgID, account.CycleStartedAt, ownerID, "")
			accrued := s.scopedAccrual(ctx, repo, orgID, account.CycleStartedAt, ownerID, "")
			if spentErr != nil {
				logger.S().Warnw("billing: user spend read failed, degrading to unlimited",
					"org", orgID, "owner", ownerID, "err", spentErr)
				spent = 0
			} else if scopedSpendReached(spent, accrued, limit) {
				eval.allowed, eval.reason = false, "insufficient_funds"
			} else if bound, ok := scopeBound(userScope(ownerID), limit, spent, accrued); ok {
				eval.bounds = append(eval.bounds, bound)
			}
			eval.ownerSpent = spent
			eval.scopedSpent = spent
		}
	}
	if projectID != "" {
		limit, err := s.spendLimitResolver.ProjectMonthlySpendLimitCents(ctx, projectID)
		if err != nil {
			logger.S().Warnw("billing: project spend limit read failed, degrading to unlimited",
				"org", orgID, "project", projectID, "err", err)
		} else {
			eval.effectiveLimit = minNonZeroLimit(eval.effectiveLimit, limit)
			spent, spentErr := repo.ScopedSpentCents(ctx, orgID, account.CycleStartedAt, "", projectID)
			accrued := s.scopedAccrual(ctx, repo, orgID, account.CycleStartedAt, "", projectID)
			if spentErr != nil {
				logger.S().Warnw("billing: project spend read failed, degrading to unlimited",
					"org", orgID, "project", projectID, "err", spentErr)
				spent = 0
			} else if scopedSpendReached(spent, accrued, limit) {
				eval.allowed, eval.reason = false, "insufficient_funds"
			} else if bound, ok := scopeBound(projectScope(projectID), limit, spent, accrued); ok {
				eval.bounds = append(eval.bounds, bound)
			}
			eval.projectSpent = spent
			if ownerID == "" {
				eval.scopedSpent = spent
			}
		}
	}
	// Member tier: the project administrator's cap on this person inside this
	// project. It is measured against the same person's spend in the project,
	// so a generous teammate never eats into this member's cap.
	if ownerID != "" && projectID != "" {
		limit, limitErr := s.spendLimitResolver.ProjectMemberMonthlySpendLimitCents(ctx, projectID, ownerID)
		if limitErr != nil {
			logger.S().Warnw("billing: project member spend limit read failed, degrading to unlimited",
				"org", orgID, "project", projectID, "owner", ownerID, "err", limitErr)
		} else {
			eval.effectiveLimit = minNonZeroLimit(eval.effectiveLimit, limit)
			spent, spentErr := repo.ScopedSpentCents(ctx, orgID, account.CycleStartedAt, ownerID, projectID)
			accrued := s.scopedAccrual(ctx, repo, orgID, account.CycleStartedAt, ownerID, projectID)
			if spentErr != nil {
				logger.S().Warnw("billing: project member spend read failed, degrading to unlimited",
					"org", orgID, "project", projectID, "owner", ownerID, "err", spentErr)
				spent = 0
			} else if scopedSpendReached(spent, accrued, limit) {
				eval.allowed, eval.reason = false, "insufficient_funds"
			} else if bound, ok := scopeBound(projectMemberScope(projectID, ownerID), limit, spent, accrued); ok {
				eval.bounds = append(eval.bounds, bound)
			}
			eval.memberSpent = spent
		}
	}
	// Feature #26 (AD9): publish the billing.spend_limit_breached
	// notification event best-effort when the cross-mode spend limit is
	// reached. A publish failure is logged and never fails the check.
	if !eval.allowed && account.MonthlySpendLimitCents > 0 &&
		scopedSpendReached(account.SpentThisCycleCents, inFlight, account.MonthlySpendLimitCents) {
		s.publishNotificationEvent(ctx, orgID, webhook.EventSpendLimitBreached, "spend-"+account.ID,
			map[string]any{
				"account_id":      account.ID,
				"organization_id": orgID,
				"spent_cents":     account.SpentThisCycleCents,
				"limit_cents":     account.MonthlySpendLimitCents,
			})
	}
	return eval, nil
}

// accountCeilingMicros is the settled-spend headroom of the account itself,
// in micros, before the open hour's accrual is subtracted: the prepaid
// balance, or the postpaid quota headroom, tightened by the cross-mode cap.
func accountCeilingMicros(a *Account) int64 {
	if a == nil {
		return 0
	}
	ceiling := int64(math.MaxInt64)
	if a.Mode == AccountModePrepaid {
		ceiling = CentsToMicros(a.BalanceCents)
	} else if a.MonthlyQuotaCents > 0 {
		ceiling = CentsToMicros(a.MonthlyQuotaCents - a.UsedThisCycleCents)
	}
	if a.MonthlySpendLimitCents > 0 {
		if remaining := CentsToMicros(a.MonthlySpendLimitCents - a.SpentThisCycleCents); remaining < ceiling {
			ceiling = remaining
		}
	}
	return ceiling
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

// reasonNoBillingAccount is the denial an unfunded tenant gets once
// billing.funds.enforceDefault is on. It is distinct from insufficient_funds
// so an operator reading a gateway log can tell "nobody opened an account"
// from "the balance ran out".
const reasonNoBillingAccount = "no_billing_account"

// scopedSpendReached reports whether one scope's settled spend plus its
// in-flight accrual has reached the cap. A non-positive cap means unlimited.
func scopedSpendReached(spentCents, accruedMicros, limitCents int64) bool {
	if limitCents <= 0 {
		return false
	}
	return CentsToMicros(spentCents)+accruedMicros >= CentsToMicros(limitCents)
}

// scopedAccrual reads the in-flight spend of one attribution scope. A read
// failure degrades to the settled-only answer with a log line: an accounting
// hiccup must not turn into a billing outage.
func (s *Service) scopedAccrual(ctx context.Context, repo *AccountRepository, orgID string, since int64, ownerUserID, projectID string) int64 {
	micros, err := repo.AccrualMicros(ctx, orgID, since, ownerUserID, projectID)
	if err != nil {
		logger.S().Warnw("billing: accrual read failed, gating on settled spend only",
			"org", orgID, "owner", ownerUserID, "project", projectID, "err", err)
		return 0
	}
	return micros
}

// fundsAllowed evaluates the gating truth table (AC6/AC7): prepaid
// blocked once the balance is drained; postpaid blocked at quota exceeded
// only under the "block" policy; the cross-mode spend limit blocks when
// reached (feature #11, AD5/AD7).
//
// accruedMicros is money already spent on usage that has not been settled
// yet (the current hour). Counting it is what stops a caller from running a
// drained balance negative before the next settlement pass: available funds
// are "what the ledger says" minus "what is already in flight".
func fundsAllowed(a *Account, accruedMicros int64) (bool, string) {
	if a.MonthlySpendLimitCents > 0 &&
		CentsToMicros(a.SpentThisCycleCents)+accruedMicros >= CentsToMicros(a.MonthlySpendLimitCents) {
		return false, "insufficient_funds"
	}
	if a.Mode == AccountModePrepaid {
		if CentsToMicros(a.BalanceCents)-accruedMicros <= 0 {
			return false, "insufficient_funds"
		}
		return true, ""
	}
	if a.MonthlyQuotaCents > 0 &&
		CentsToMicros(a.UsedThisCycleCents)+accruedMicros >= CentsToMicros(a.MonthlyQuotaCents) {
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
	// A platform administrator owns no tenant, so the session resolves to an
	// empty organization — but browsing a tenant's money page is exactly what
	// the org header is for on the admin surface. The allowance is limited to
	// that role: for anyone else an empty session organization is authoritative
	// (feature-17 AD6).
	if orgID == "" && authctx.IsPlatformAdmin(ctx) {
		if fromHeader, headerErr := resolveOrganizationID(ctx); headerErr == nil {
			orgID = fromHeader
		}
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
	// An empty scope means there is no tenant to report on at all — that stays an
	// error so an unfunded lookup cannot be confused with a missing scope.
	if orgID == "" {
		return nil, apierrors.New(apierrors.CodeAccountNotFound)
	}
	// A tenant is onboarded before it is funded, so "no account yet" is a normal
	// state of a real organization rather than an error: the console renders the
	// unfunded card from has_account=false (feature-18 AD3).
	if account == nil {
		return &billingv1.GetBalanceResponse{
			Response:   okResponse(),
			Currency:   config.GetConfig().Billing.Currency,
			HasAccount: false,
		}, nil
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

// userLabels resolves administrator ids to display labels in one call. A
// failure (or an unwired resolver) yields no labels: the ledger still shows the
// ids, which is diagnostic data, not something to fail a page over.
func (s *Service) userLabels(ctx context.Context, userIDs []string) map[string]string {
	if s.userLabelResolver == nil || len(userIDs) == 0 {
		return nil
	}
	labels, err := s.userLabelResolver.ResolveUserLabels(ctx, userIDs)
	if err != nil {
		return nil
	}
	return labels
}
