package mq

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
)

// NewClient builds a Client from configuration. An unknown driver is an
// error: silently falling back would hide deployment mistakes.
func NewClient(cfg *config.MQConfig) (Client, error) {
	switch strings.ToLower(cfg.Driver) {
	case "rabbitmq":
		return newRabbitMQClient(cfg)
	case "", "fake":
		// Explicit "fake" (or empty driver) keeps local development and
		// unit tests working without a broker.
		return NewFake(), nil
	default:
		return nil, fmt.Errorf("mq: unsupported driver %q", cfg.Driver)
	}
}

// amqpConn and amqpChannel are the minimal broker surfaces the client
// depends on. They are satisfied by *amqp.Connection / *amqp.Channel and
// by the in-memory fake used in unit tests.
type amqpConn interface {
	Channel() (amqpChannel, error)
	Close() error
	IsClosed() bool
}

type amqpChannel interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	Close() error
}

// realConn adapts *amqp.Connection to amqpConn.
type realConn struct{ *amqp.Connection }

func (c realConn) Channel() (amqpChannel, error) {
	ch, err := c.Connection.Channel()
	if err != nil {
		return nil, err
	}
	return realChannel{ch}, nil
}

// realChannel adapts *amqp.Channel to amqpChannel.
type realChannel struct{ *amqp.Channel }

// dialFunc opens a broker connection. It is a package variable so tests can
// inject a fake broker.
var dialFunc = func(url string) (amqpConn, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	return realConn{conn}, nil
}

// rabbitMQClient implements Client over a RabbitMQ broker. Subjects map to
// a topic exchange + routing key; the namespace prefixes the exchange name
// so environments sharing one broker never see each other's traffic.
type rabbitMQClient struct {
	url       string
	namespace string

	mu     sync.Mutex
	conn   amqpConn
	closed bool
}

func newRabbitMQClient(cfg *config.MQConfig) (*rabbitMQClient, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("mq: rabbitmq url is empty")
	}
	return &rabbitMQClient{url: cfg.URL, namespace: cfg.Namespace}, nil
}

// exchange returns the namespaced topic exchange name.
func (c *rabbitMQClient) exchange() string {
	if c.namespace == "" {
		return "taas"
	}
	return c.namespace
}

// connect opens (or reopens) the connection and declares the topic
// exchange. It is safe to call concurrently; the caller holds c.mu.
func (c *rabbitMQClient) connect() error {
	if c.conn != nil && !c.conn.IsClosed() {
		return nil
	}
	conn, err := dialFunc(c.url)
	if err != nil {
		return fmt.Errorf("mq: connect rabbitmq %s: %w", c.url, err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mq: open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()
	if err := ch.ExchangeDeclare(
		c.exchange(),
		"topic",
		true,  // durable
		false, // auto-delete
		false, // internal
		false, // no-wait
		nil,
	); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mq: declare exchange %s: %w", c.exchange(), err)
	}
	c.conn = conn
	return nil
}

// Publish sends a message to the topic exchange with the subject as the
// routing key. It reconnects on a stale channel.
func (c *rabbitMQClient) Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mq: rabbitmq client closed")
	}
	if err := c.connect(); err != nil {
		return err
	}
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("mq: open publish channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	table := amqp.Table{}
	for k, v := range headers {
		table[k] = v
	}
	return ch.PublishWithContext(
		ctx,
		c.exchange(),
		subject,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/octet-stream",
			Body:         body,
			Headers:      table,
			DeliveryMode: amqp.Persistent,
			Timestamp:    time.Now(),
		},
	)
}

// Subscribe declares a per-consumer queue, binds it to the topic exchange
// with the subject as the binding key, and consumes until ctx is cancelled.
// It reconnects forever so the consumer survives broker restarts.
func (c *rabbitMQClient) Subscribe(ctx context.Context, subject string, handler Handler) error {
	for {
		if err := c.subscribeOnce(ctx, subject, handler); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.S().Warnw("mq: rabbitmq consumer loop error, retrying", "subject", subject, "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		return nil
	}
}

func (c *rabbitMQClient) subscribeOnce(ctx context.Context, subject string, handler Handler) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("mq: rabbitmq client closed")
	}
	if err := c.connect(); err != nil {
		c.mu.Unlock()
		return err
	}
	conn := c.conn
	c.mu.Unlock()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("mq: open consume channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	// A per-consumer, auto-delete queue. The queue name is derived from
	// the subject so a reconnect re-binds the same queue.
	q, err := ch.QueueDeclare(
		"taas."+subject,
		false, // durable
		true,  // auto-delete
		false, // exclusive
		false, // no-wait
		nil,
	)
	if err != nil {
		return fmt.Errorf("mq: declare queue: %w", err)
	}
	if err := ch.QueueBind(q.Name, subject, c.exchange(), false, nil); err != nil {
		return fmt.Errorf("mq: bind queue %s to %s: %w", q.Name, subject, err)
	}

	deliveries, err := ch.Consume(
		q.Name,
		"",    // auto-generated consumer tag
		false, // auto-ack off; we ack explicitly after a successful handler
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,
	)
	if err != nil {
		return fmt.Errorf("mq: consume %s: %w", q.Name, err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				// Channel closed (broker restart); reconnect.
				return fmt.Errorf("mq: consume channel closed for %s", subject)
			}
			headers := make(map[string]string, len(d.Headers))
			for k, v := range d.Headers {
				if s, ok := v.(string); ok {
					headers[k] = s
				} else {
					headers[k] = fmt.Sprintf("%v", v)
				}
			}
			msg := Message{
				Subject:   subject,
				Body:      d.Body,
				Headers:   headers,
				Timestamp: d.Timestamp,
			}
			if err := handler(msg); err != nil {
				logger.S().Warnw("mq: handler error", "subject", subject, "err", err)
				if IsPermanent(err) {
					// Reject without requeue: the message can never
					// succeed.
					_ = d.Nack(false, false)
				} else {
					// Requeue for broker-level retry.
					_ = d.Nack(false, true)
				}
			} else {
				_ = d.Ack(false)
			}
		}
	}
}

// Close closes the underlying connection.
func (c *rabbitMQClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.conn != nil && !c.conn.IsClosed() {
		return c.conn.Close()
	}
	return nil
}
