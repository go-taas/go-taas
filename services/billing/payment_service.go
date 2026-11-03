package billing

import (
	"context"
	"strings"

	"google.golang.org/grpc"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/webhook"
)

// paymentRepository lazily wires and returns the payment repository.
func (s *Service) paymentRepository() (*PaymentRepository, error) {
	if s.paymentRepo != nil {
		return s.paymentRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.paymentRepo = NewPaymentRepository(db)
	return s.paymentRepo, nil
}

// invoiceRepository lazily wires and returns the invoice repository.
func (s *Service) invoiceRepository() (*InvoiceRepository, error) {
	if s.invoiceRepo != nil {
		return s.invoiceRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.invoiceRepo = NewInvoiceRepository(db)
	return s.invoiceRepo, nil
}

// CreatePaymentChannel registers a payment channel (feature-14 FR1.1).
func (s *Service) CreatePaymentChannel(ctx context.Context, req *billingv1.CreatePaymentChannelRequest) (*billingv1.CreatePaymentChannelResponse, error) {
	repo, err := s.paymentRepository()
	if err != nil {
		return nil, err
	}
	ch := &PaymentChannel{
		ID:          strings.TrimSpace(req.GetChannelId()),
		Type:        strings.TrimSpace(req.GetType()),
		DisplayName: strings.TrimSpace(req.GetDisplayName()),
		Enabled:     true,
	}
	if ch.ID == "" || ch.Type == "" || ch.DisplayName == "" {
		return nil, apierrors.New(apierrors.CodePaymentChannelInvalid)
	}
	if err := repo.CreateChannel(ctx, ch); err != nil {
		return nil, err
	}
	return &billingv1.CreatePaymentChannelResponse{
		Response: okResponse(),
		Channel:  summarizeChannel(ch),
	}, nil
}

// ListPaymentChannels returns the channel registry (feature-14 FR1.1).
func (s *Service) ListPaymentChannels(ctx context.Context, req *billingv1.ListPaymentChannelsRequest) (*billingv1.ListPaymentChannelsResponse, error) {
	repo, err := s.paymentRepository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.ListChannels(ctx, offset, limit)
	if err != nil {
		return nil, err
	}
	channels := make([]*billingv1.PaymentChannel, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, summarizeChannel(row))
	}
	return &billingv1.ListPaymentChannelsResponse{
		Response: okResponse(),
		Channels: channels,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// EnablePaymentChannel activates a channel (feature-14 FR1.1).
func (s *Service) EnablePaymentChannel(ctx context.Context, req *billingv1.EnablePaymentChannelRequest) (*billingv1.EnablePaymentChannelResponse, error) {
	repo, err := s.paymentRepository()
	if err != nil {
		return nil, err
	}
	ch, err := repo.SetChannelEnabled(ctx, req.GetChannelId(), true)
	if err != nil {
		return nil, err
	}
	return &billingv1.EnablePaymentChannelResponse{Response: okResponse(), Channel: summarizeChannel(ch)}, nil
}

// DisablePaymentChannel pauses a channel (feature-14 FR1.1).
func (s *Service) DisablePaymentChannel(ctx context.Context, req *billingv1.DisablePaymentChannelRequest) (*billingv1.DisablePaymentChannelResponse, error) {
	repo, err := s.paymentRepository()
	if err != nil {
		return nil, err
	}
	ch, err := repo.SetChannelEnabled(ctx, req.GetChannelId(), false)
	if err != nil {
		return nil, err
	}
	return &billingv1.DisablePaymentChannelResponse{Response: okResponse(), Channel: summarizeChannel(ch)}, nil
}

// CreatePaymentIntent creates a payment intent for a prepaid account
// (feature-14 FR1.2).
func (s *Service) CreatePaymentIntent(ctx context.Context, req *billingv1.CreatePaymentIntentRequest) (*billingv1.CreatePaymentIntentResponse, error) {
	amount := req.GetAmountCents()
	if amount <= 0 {
		return nil, apierrors.New(apierrors.CodePaymentIntentInvalid)
	}
	channel := strings.TrimSpace(req.GetChannel())
	if channel == "" {
		return nil, apierrors.New(apierrors.CodePaymentIntentInvalid)
	}
	key := strings.TrimSpace(req.GetIdempotencyKey())
	if key == "" {
		return nil, apierrors.New(apierrors.CodePaymentIntentInvalid)
	}
	accountRepo, err := s.accountRepository()
	if err != nil {
		return nil, err
	}
	account, err := accountRepo.FindByID(ctx, req.GetAccountId())
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, apierrors.New(apierrors.CodeAccountNotFound)
	}
	if account.Mode != AccountModePrepaid {
		return nil, apierrors.New(apierrors.CodePaymentIntentInvalid)
	}
	payRepo, err := s.paymentRepository()
	if err != nil {
		return nil, err
	}
	intent := &PaymentIntent{
		AccountID:      account.ID,
		AmountCents:    amount,
		Channel:        channel,
		IdempotencyKey: key,
		Reference:      strings.TrimSpace(req.GetReference()),
	}
	if err := payRepo.CreateIntent(ctx, intent); err != nil {
		return nil, err
	}
	return &billingv1.CreatePaymentIntentResponse{
		Response: okResponse(),
		Intent:   summarizeIntent(intent),
	}, nil
}

// PayPaymentIntent completes a payment intent via the mock channel and
// credits the account exactly once (feature-14 FR1.3, AC2).
func (s *Service) PayPaymentIntent(ctx context.Context, req *billingv1.PayPaymentIntentRequest) (*billingv1.PayPaymentIntentResponse, error) {
	payRepo, err := s.paymentRepository()
	if err != nil {
		return nil, err
	}
	intent, err := payRepo.FindIntentByID(ctx, req.GetIntentId())
	if err != nil {
		return nil, err
	}
	if intent == nil {
		return nil, apierrors.New(apierrors.CodePaymentIntentInvalid)
	}
	if intent.Status == IntentStatusPaid {
		return &billingv1.PayPaymentIntentResponse{Response: okResponse(), Intent: summarizeIntent(intent)}, nil
	}
	accountRepo, err := s.accountRepository()
	if err != nil {
		return nil, err
	}
	account, err := accountRepo.FindByID(ctx, intent.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, apierrors.New(apierrors.CodeAccountNotFound)
	}
	// Mark paid and credit the account (idempotent via the intent guard
	// and the recharge idempotency key).
	paid, err := payRepo.MarkIntentPaid(ctx, intent, account, intent.AmountCents)
	if err != nil {
		return nil, err
	}
	// Credit the account via the recharge path with a payment-scoped key.
	_, _, err = accountRepo.Recharge(ctx, account, intent.AmountCents,
		"payment:"+intent.ID,
		MoneyMovement{Note: "payment intent " + intent.ID, Source: SourcePayment},
		TransactionTypeRecharge)
	if err != nil {
		return nil, err
	}
	// Feature #23 (AD10): publish the billing.invoice_paid webhook event
	// best-effort.
	s.publishWebhookEvent(ctx, account.OrganizationID, webhook.EventInvoicePaid, "pay-"+intent.ID,
		map[string]any{
			"intent_id":       intent.ID,
			"organization_id": account.OrganizationID,
			"amount_cents":    intent.AmountCents,
		})
	// Feature #26 (AD9): publish the same event to notification.events.
	s.publishNotificationEvent(ctx, account.OrganizationID, webhook.EventInvoicePaid, "pay-"+intent.ID,
		map[string]any{
			"intent_id":       intent.ID,
			"organization_id": account.OrganizationID,
			"amount_cents":    intent.AmountCents,
		})
	return &billingv1.PayPaymentIntentResponse{Response: okResponse(), Intent: summarizeIntent(paid)}, nil
}

// ListPaymentIntents returns payment intents (feature-14 FR1).
func (s *Service) ListPaymentIntents(ctx context.Context, req *billingv1.ListPaymentIntentsRequest) (*billingv1.ListPaymentIntentsResponse, error) {
	payRepo, err := s.paymentRepository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := payRepo.ListIntents(ctx, req.GetAccountId(), req.GetStatus(), offset, limit)
	if err != nil {
		return nil, err
	}
	intents := make([]*billingv1.PaymentIntent, 0, len(rows))
	for _, row := range rows {
		intents = append(intents, summarizeIntent(row))
	}
	return &billingv1.ListPaymentIntentsResponse{
		Response: okResponse(),
		Intents:  intents,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampInt32(limit)},
	}, nil
}

// summarizeChannel maps a channel row to the proto summary.
func summarizeChannel(ch *PaymentChannel) *billingv1.PaymentChannel {
	return &billingv1.PaymentChannel{
		ChannelId:   ch.ID,
		Type:        ch.Type,
		DisplayName: ch.DisplayName,
		Enabled:     ch.Enabled,
		CreatedAt:   ch.CreatedAt.Unix(),
	}
}

// summarizeIntent maps an intent row to the proto summary.
func summarizeIntent(in *PaymentIntent) *billingv1.PaymentIntent {
	paidAt := int64(0)
	if in.PaidAt != nil {
		paidAt = in.PaidAt.Unix()
	}
	payURL := ""
	if in.Channel == mockChannelID {
		payURL = "/api/v1/admin/billing/payment-intents/" + in.ID + ":pay"
	}
	return &billingv1.PaymentIntent{
		IntentId:       in.ID,
		AccountId:      in.AccountID,
		AmountCents:    in.AmountCents,
		Channel:        in.Channel,
		Status:         in.Status,
		IdempotencyKey: in.IdempotencyKey,
		Reference:      in.Reference,
		CreatedAt:      in.CreatedAt.Unix(),
		PaidAt:         paidAt,
		PayUrl:         payURL,
	}
}

// accountRepository lazily wires and returns the account repository.
func (s *Service) accountRepository() (*AccountRepository, error) {
	if s.accounts != nil {
		return s.accounts, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.accounts = NewAccountRepository(db)
	return s.accounts, nil
}

// AttachToServer implements server.Service (feature-14 adds no new
// gRPC server registration; the RPCs are on BillingService).
var _ = grpc.Server{}
var _ = server.Service(nil)
