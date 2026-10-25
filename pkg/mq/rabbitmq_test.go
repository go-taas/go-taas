package mq

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
)

// fakeChannel is an in-memory amqpChannel that records published messages
// and delivers them to a registered consumer.
type fakeChannel struct {
	// prefetch is the last Qos request (0 = unlimited).
	prefetch   int
	mu         sync.Mutex
	closed     bool
	exchange   string
	kind       string
	queue      string
	bindKey    string
	deliveries chan amqp.Delivery
	published  []amqp.Publishing
}

func newFakeChannel() *fakeChannel {
	return &fakeChannel{deliveries: make(chan amqp.Delivery, 16)}
}

// Qos records the prefetch request; the fake has no in-flight limit.
func (f *fakeChannel) Qos(prefetchCount, _ int, _ bool) error {
	f.prefetch = prefetchCount
	return nil
}

func (f *fakeChannel) ExchangeDeclare(name, kind string, _, _, _, _ bool, _ amqp.Table) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchange = name
	f.kind = kind
	return nil
}

func (f *fakeChannel) QueueDeclare(name string, _, _, _, _ bool, _ amqp.Table) (amqp.Queue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = name
	return amqp.Queue{Name: name}, nil
}

func (f *fakeChannel) QueueBind(_, key, _ string, _ bool, _ amqp.Table) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindKey = key
	return nil
}

func (f *fakeChannel) Consume(_, _ string, _, _, _, _ bool, _ amqp.Table) (<-chan amqp.Delivery, error) {
	return f.deliveries, nil
}

func (f *fakeChannel) PublishWithContext(_ context.Context, _, _ string, _, _ bool, msg amqp.Publishing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errors.New("channel closed")
	}
	f.published = append(f.published, msg)
	return nil
}

func (f *fakeChannel) Close() error {
	// No-op: the fake channel is shared across calls, so closing it would
	// break subsequent Publish/Subscribe on the same fake connection.
	return nil
}

// fakeConn is an in-memory amqpConn.
type fakeConn struct {
	mu      sync.Mutex
	closed  bool
	channel *fakeChannel
}

func (f *fakeConn) Channel() (amqpChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("connection closed")
	}
	return f.channel, nil
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConn) IsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// installFakeBroker swaps dialFunc for a fake broker and returns a cleanup.
func installFakeBroker(t *testing.T) *fakeConn {
	t.Helper()
	conn := &fakeConn{channel: newFakeChannel()}
	old := dialFunc
	dialFunc = func(string) (amqpConn, error) { return conn, nil }
	t.Cleanup(func() { dialFunc = old })
	return conn
}

func TestRabbitMQPublish(t *testing.T) {
	conn := installFakeBroker(t)
	c, err := newRabbitMQClient(&config.MQConfig{Driver: "rabbitmq", URL: "amqp://guest:guest@127.0.0.1:5672/", Namespace: "taas-dev"})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()

	require.NoError(t, c.Publish(context.Background(), "a.b", []byte("hello"), map[string]string{"k": "v"}))
	require.Len(t, conn.channel.published, 1)
	assert.Equal(t, "hello", string(conn.channel.published[0].Body))
	assert.Equal(t, "v", conn.channel.published[0].Headers["k"])
	assert.Equal(t, "taas-dev", conn.channel.exchange)
	assert.Equal(t, "topic", conn.channel.kind)
}

func TestRabbitMQPublishConnectError(t *testing.T) {
	old := dialFunc
	dialFunc = func(string) (amqpConn, error) { return nil, errors.New("dial failed") }
	defer func() { dialFunc = old }()

	c, err := newRabbitMQClient(&config.MQConfig{Driver: "rabbitmq", URL: "amqp://guest:guest@127.0.0.1:5672/"})
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	assert.Error(t, c.Publish(context.Background(), "a.b", []byte("x"), nil))
}

func TestRabbitMQSubscribeDelivers(t *testing.T) {
	conn := installFakeBroker(t)
	c, err := newRabbitMQClient(&config.MQConfig{Driver: "rabbitmq", URL: "amqp://guest:guest@127.0.0.1:5672/", Namespace: "taas-dev"})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got []Message
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		_ = c.Subscribe(ctx, "a.b", func(msg Message) error {
			mu.Lock()
			got = append(got, msg)
			mu.Unlock()
			return nil
		})
		close(done)
	}()

	// Wait for the consumer to be set up.
	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.channel.queue == "taas.a.b"
	}, 2*time.Second, 10*time.Millisecond)

	conn.channel.deliveries <- amqp.Delivery{
		Body:      []byte("payload"),
		Headers:   amqp.Table{"k": "v"},
		Timestamp: time.Now(),
	}

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}, 2*time.Second, 10*time.Millisecond)

	mu.Lock()
	assert.Equal(t, "a.b", got[0].Subject)
	assert.Equal(t, "payload", string(got[0].Body))
	assert.Equal(t, "v", got[0].Headers["k"])
	mu.Unlock()

	cancel()
	<-done
}

func TestRabbitMQSubscribeHandlerError(t *testing.T) {
	conn := installFakeBroker(t)
	c, err := newRabbitMQClient(&config.MQConfig{Driver: "rabbitmq", URL: "amqp://guest:guest@127.0.0.1:5672/", Namespace: "taas-dev"})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = c.Subscribe(ctx, "a.b", func(Message) error { return errors.New("boom") })
		close(done)
	}()

	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.channel.queue == "taas.a.b"
	}, 2*time.Second, 10*time.Millisecond)

	conn.channel.deliveries <- amqp.Delivery{Body: []byte("x")}
	// Give the handler time to run; the error path must not panic.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
}

func TestRabbitMQSubscribePermanentError(t *testing.T) {
	conn := installFakeBroker(t)
	c, err := newRabbitMQClient(&config.MQConfig{Driver: "rabbitmq", URL: "amqp://guest:guest@127.0.0.1:5672/", Namespace: "taas-dev"})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = c.Subscribe(ctx, "a.b", func(Message) error { return Permanent(errors.New("bad")) })
		close(done)
	}()

	require.Eventually(t, func() bool {
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return conn.channel.queue == "taas.a.b"
	}, 2*time.Second, 10*time.Millisecond)

	conn.channel.deliveries <- amqp.Delivery{Body: []byte("x")}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
}

func TestRabbitMQSubscribeClosedClient(t *testing.T) {
	installFakeBroker(t)
	c, err := newRabbitMQClient(&config.MQConfig{Driver: "rabbitmq", URL: "amqp://guest:guest@127.0.0.1:5672/", Namespace: "taas-dev"})
	require.NoError(t, err)
	require.NoError(t, c.Close())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// subscribeOnce on a closed client returns an error; Subscribe retries
	// until ctx is cancelled.
	done := make(chan struct{})
	go func() {
		_ = c.Subscribe(ctx, "a.b", func(Message) error { return nil })
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func TestRabbitMQCloseIdempotent(t *testing.T) {
	conn := installFakeBroker(t)
	c, err := newRabbitMQClient(&config.MQConfig{Driver: "rabbitmq", URL: "amqp://guest:guest@127.0.0.1:5672/", Namespace: "taas-dev"})
	require.NoError(t, err)
	// Establish a connection so Close has something to release.
	require.NoError(t, c.Publish(context.Background(), "a.b", []byte("x"), nil))
	require.NoError(t, c.Close())
	require.NoError(t, c.Close())
	assert.True(t, conn.IsClosed())
}

// TestSubscribeSetsPrefetch verifies the consumer bounds its unacked
// window: without a QoS limit the broker hands the whole backlog over, so
// one slow handler leaves every queued message unacked (a queue purge
// cannot reclaim them and no other consumer can pick them up).
func TestSubscribeSetsPrefetch(t *testing.T) {
	conn := installFakeBroker(t)
	c, err := newRabbitMQClient(&config.MQConfig{
		Driver: "rabbitmq", URL: "amqp://127.0.0.1:5672/", Namespace: "taas-dev",
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = c.Subscribe(ctx, "infer.services.changes", func(Message) error { return nil })
	}()

	require.Eventually(t, func() bool { return conn.channel.prefetch != 0 },
		2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, conn.channel.prefetch)
}
