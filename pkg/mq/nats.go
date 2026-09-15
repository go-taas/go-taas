package mq

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
)

// NewClient builds a Client from configuration. An unknown driver is an
// error: silently falling back would hide deployment mistakes.
func NewClient(cfg *config.MQConfig) (Client, error) {
	switch strings.ToLower(cfg.Driver) {
	case "nats":
		return newNATSClient(cfg)
	case "", "fake":
		// Explicit "fake" (or empty driver) keeps local development and
		// unit tests working without a broker.
		return NewFake(), nil
	default:
		return nil, fmt.Errorf("mq: unsupported driver %q", cfg.Driver)
	}
}

// natsClient implements Client over a NATS connection. Subjects are
// prefixed with the configured namespace.
type natsClient struct {
	conn      *nats.Conn
	namespace string
}

func newNATSClient(cfg *config.MQConfig) (*natsClient, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("mq: nats url is empty")
	}
	opts := []nats.Option{
		nats.MaxReconnects(-1), // reconnect forever; the consumer loop survives broker restarts
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.S().Warnw("mq: nats disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			logger.S().Infow("mq: nats reconnected", "url", c.ConnectedUrl())
		}),
	}
	conn, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("mq: connect nats %s: %w", cfg.URL, err)
	}
	return &natsClient{conn: conn, namespace: cfg.Namespace}, nil
}

func (c *natsClient) subject(subject string) string {
	if c.namespace == "" {
		return subject
	}
	return c.namespace + "." + subject
}

// Publish sends a message to the namespaced subject.
func (c *natsClient) Publish(_ context.Context, subject string, body []byte, headers map[string]string) error {
	msg := nats.NewMsg(c.subject(subject))
	msg.Data = body
	for k, v := range headers {
		msg.Header.Set(k, v)
	}
	return c.conn.PublishMsg(msg)
}

// Subscribe registers handler on the namespaced subject. It blocks until
// ctx is cancelled; the subscription is drained on exit.
func (c *natsClient) Subscribe(ctx context.Context, subject string, handler Handler) error {
	sub, err := c.conn.Subscribe(c.subject(subject), func(m *nats.Msg) {
		headers := make(map[string]string, len(m.Header))
		for k := range m.Header {
			headers[k] = m.Header.Get(k)
		}
		msg := Message{
			Subject:   subject,
			Body:      m.Data,
			Headers:   headers,
			Timestamp: time.Now(),
		}
		if err := handler(msg); err != nil {
			logger.S().Warnw("mq: handler error", "subject", subject, "err", err)
		}
	})
	if err != nil {
		return fmt.Errorf("mq: subscribe %s: %w", subject, err)
	}

	<-ctx.Done()
	_ = sub.Unsubscribe()
	return nil
}

// Close drains and closes the underlying connection.
func (c *natsClient) Close() error {
	c.conn.Close()
	return nil
}
