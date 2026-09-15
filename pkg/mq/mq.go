// Package mq provides the message-queue abstraction used to decouple the
// gRPC server from the controller: the server publishes desired-state
// changes, the controller consumes them and drives Kubernetes resources.
//
// The interface is broker-agnostic; concrete implementations live in this
// package (NATS today, Kafka planned) and tests use the in-memory fake.
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
	Subscribe(ctx context.Context, subject string, handler Handler) error
	// Close releases broker resources.
	Close() error
}

// Subjects groups the canonical subjects used by the platform. All subjects
// are prefixed with the configured namespace at the client construction
// site, so environments sharing one broker never see each other's traffic.
type Subjects struct {
	// InferServiceChanges carries inference-service desired-state changes.
	InferServiceChanges string
	// ImageWarmups carries image warmup tasks.
	ImageWarmups string
	// MeteringEvents carries token usage events from the data-plane gateway.
	MeteringEvents string
	// Settlements carries settlement events from metering to billing.
	Settlements string
}

// DefaultSubjects returns the canonical subject names.
func DefaultSubjects() Subjects {
	return Subjects{
		InferServiceChanges: "infer.services.changes",
		ImageWarmups:        "image.warmups",
		MeteringEvents:      "metering.events",
		Settlements:         "billing.settlements",
	}
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

func (f *fakeClient) Subscribe(_ context.Context, subject string, handler Handler) error {
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
