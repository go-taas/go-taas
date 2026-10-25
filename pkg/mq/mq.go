// Package mq provides the message-queue abstraction used to decouple the
// gRPC server from the controller: the server publishes desired-state
// changes, the controller consumes them and drives Kubernetes resources.
//
// The interface is broker-agnostic; concrete implementations live in this
// package (RabbitMQ today, Kafka planned) and tests use the in-memory fake.
package mq

import (
	"context"
	"errors"
	"sync"
	"time"
)

// permanentError marks a delivery error as non-retryable.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks a delivery error as non-retryable. Handlers return it to
// stop broker-level retry loops for messages that can never succeed.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// IsPermanent reports whether err was marked as non-retryable.
func IsPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}

// Message is the unit of communication between publishers and consumers.
type Message struct {
	// Subject is the topic/routing key the message is published to.
	Subject string
	// Body is the opaque payload (typically JSON or protobuf).
	Body []byte
	// Headers carry optional metadata (event type, trace id, ...).
	Headers map[string]string
	// Timestamp records when the message was published.
	Timestamp time.Time
}

// Handler processes a single message. Returning a non-nil error triggers
// broker-level retry (when supported by the implementation); wrap the error
// with Permanent to reject the message instead.
type Handler func(msg Message) error

// Client is the message-queue abstraction shared by publishers (the gRPC
// server) and consumers (the controller).
type Client interface {
	// Publish sends a message to the given subject.
	Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error
	// Subscribe registers handler for the subject and blocks until ctx is
	// cancelled or the underlying connection fails.
	//
	// Two subscribers of one subject share a single queue and therefore
	// *split* the messages between them, which is only correct when they run
	// the same handler. An independent consumer of a subject another service
	// also consumes must go through SubscribeInGroup.
	Subscribe(ctx context.Context, subject string, handler Handler) error
	// Close releases broker resources.
	Close() error
}

// GroupedSubscriber is implemented by clients that can bind a queue per
// consumer group, so independent consumers of one subject each receive every
// message instead of splitting them.
//
// It is a separate interface rather than a Client method because subscribing
// is the only thing that varies: a client without the capability degrades to
// plain Subscribe, which keeps third-party implementations and test doubles
// working.
type GroupedSubscriber interface {
	// SubscribeInGroup is Subscribe with an explicit group. The queue is
	// named per group, so every group sees the whole subject.
	SubscribeInGroup(ctx context.Context, subject, group string, handler Handler) error
}

// SubscribeInGroup subscribes with an explicit consumer group when the client
// supports it, and falls back to a plain Subscribe otherwise.
func SubscribeInGroup(ctx context.Context, client Client, subject, group string, handler Handler) error {
	if grouped, ok := client.(GroupedSubscriber); ok {
		return grouped.SubscribeInGroup(ctx, subject, group, handler)
	}
	return client.Subscribe(ctx, subject, handler)
}

// Subjects groups the canonical subjects used by the platform. All subjects
// are prefixed with the configured namespace at the client construction
// site, so environments sharing one broker never see each other's traffic.
type Subjects struct {
	// InferServiceChanges carries inference-service desired-state changes.
	InferServiceChanges string
	// InferServiceStatus carries observed-state reports from the
	// controller back to the infer module (state, endpoints, failures).
	InferServiceStatus string
	// InferServiceConcurrency carries gateway-reported in-flight
	// concurrency per inference service (feature #16, AD11). The
	// controller consumes it to drive the HPA concurrency metric.
	InferServiceConcurrency string
	// ImageWarmups carries image warmup tasks.
	ImageWarmups string
	// ImageWarmupStatus carries warmup task status reports from the
	// controller back to the image module (state, node results).
	ImageWarmupStatus string
	// AcceleratorInventory carries the controller's full accelerator
	// fleet snapshot (feature #18, AD1). The accelerator service
	// consumes it to maintain its in-memory projection cache.
	AcceleratorInventory string
	// MeteringEvents carries token usage events from the data-plane gateway.
	MeteringEvents string
	// Settlements carries settlement events from metering to billing.
	Settlements string
	// WebhookEvents carries the platform event catalog (feature #23,
	// AD10). The producing modules (infer for the admin events, billing
	// for the end-user events) publish a canonical WebhookEvent envelope;
	// the webhook module subscribes and routes each event to the matching
	// enabled webhooks.
	WebhookEvents string
	// NotificationEvents carries the same platform event catalog for the
	// in-console notification center (feature #26, AD9). The producing
	// modules (infer for the admin events, billing for the end-user
	// events) publish a canonical NotificationEvent envelope; the
	// notification module subscribes and creates a notification for every
	// enabled user, and evaluates threshold alerts on the same stream.
	NotificationEvents string
	// ClusterHealth carries the controller's per-cluster health snapshot
	// (feature #40, AD4). The cluster module consumes it to maintain its
	// in-memory projection cache.
	ClusterHealth string
	// InferRoutingPolicies carries routing-policy revision snapshots the
	// infer outbox publisher emits and the inference gateway adapter
	// consumes (feature #45, §9). It is not consumed by the controller.
	InferRoutingPolicies string
	// RuntimeHealth carries the controller's runtime component +
	// ModelClaim full snapshot (feature #47, AD4). The runtime service
	// consumes it to maintain its in-memory projection cache.
	RuntimeHealth string
	// RuntimeScheme carries the persisted serving scheme broadcast so the
	// DB-less controller reconverges after a restart (feature #47, AD6).
	RuntimeScheme string
	// RuntimeModelChanges carries runtime model registration desired-state
	// changes the controller reconciles into AIBrix ModelClaims
	// (feature #47, AD5).
	RuntimeModelChanges string
	// QuotaOrgsConcurrency carries per-org in-flight request counts
	// reported by the Inference Gateway (feature #51, AD14). The tenancy
	// quota consumer keeps the latest snapshot per org for the usage
	// display; zero counts are included so stale entries clear.
	QuotaOrgsConcurrency string
	// FaultSignals carries the controller's structured failure signals
	// (feature #53, AD12). The fault module consumes them to create and
	// diagnose incidents.
	FaultSignals string
	// FaultActions carries recovery actions for node/accelerator targets
	// (feature #53, AD14). The controller consumes them to execute
	// reschedule/drain.
	FaultActions string
}

// DefaultSubjects returns the canonical subject names.
func DefaultSubjects() Subjects {
	return Subjects{
		InferServiceChanges:     "infer.services.changes",
		InferServiceStatus:      "infer.services.status",
		InferServiceConcurrency: "infer.services.concurrency",
		ImageWarmups:            "image.warmups",
		ImageWarmupStatus:       "image.warmup.status",
		AcceleratorInventory:    "accelerator.inventory",
		MeteringEvents:          "metering.events",
		Settlements:             "billing.settlements",
		WebhookEvents:           "webhook.events",
		NotificationEvents:      "notification.events",
		ClusterHealth:           "cluster.health",
		InferRoutingPolicies:    "infer.routing.policies",
		RuntimeHealth:           "runtime.health",
		RuntimeScheme:           "runtime.scheme",
		RuntimeModelChanges:     "runtime.models.changes",
		QuotaOrgsConcurrency:    "quota.orgs.concurrency",
		FaultSignals:            "fault.signals",
		FaultActions:            "fault.actions"}
}

// NewFake returns an in-memory Client for unit tests and local development
// without a broker.
func NewFake() Client {
	return &fakeClient{
		subs: make(map[string][]Handler),
	}
}

type fakeClient struct {
	mu     sync.Mutex
	subs   map[string][]Handler
	closed bool
}

func (f *fakeClient) Publish(_ context.Context, subject string, body []byte, headers map[string]string) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return errors.New("mq: fake client closed")
	}
	handlers := append([]Handler(nil), f.subs[subject]...)
	f.mu.Unlock()

	msg := Message{Subject: subject, Body: body, Headers: headers, Timestamp: time.Now()}
	for _, h := range handlers {
		if err := h(msg); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeClient) Subscribe(ctx context.Context, subject string, handler Handler) error {
	return f.SubscribeInGroup(ctx, subject, "", handler)
}

// SubscribeInGroup records the handler on the subject.
//
// The fake keeps no queue per group: its purpose is to deliver to every
// handler, which is exactly the isolation a consumer group provides.
// Modelling the split that the real broker does for a shared queue here would
// make tests pass on behaviour the platform never wants.
func (f *fakeClient) SubscribeInGroup(_ context.Context, subject, _ string, handler Handler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("mq: fake client closed")
	}
	f.subs[subject] = append(f.subs[subject], handler)
	return nil
}

func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
