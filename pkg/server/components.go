package server

import (
	"context"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/database"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/redisx"
)

// components implements Components.
type components struct {
	cfg   *config.Configuration
	db    *gorm.DB
	redis *goredis.Client
	mq    mq.Client
}

// newComponents initializes the enabled components. Components that fail
// to initialize abort startup: a control-plane process without its
// database would silently corrupt state.
func newComponents(cfg *config.Configuration, opts Options) *components {
	c := &components{cfg: cfg}

	if cfg == nil {
		logger.S().Warn("components: no configuration loaded, all components disabled")
		return c
	}

	if opts.EnableComponentDB {
		db, err := database.InitDB(&cfg.Databases.Master)
		if err != nil {
			logger.S().Fatalw("init db failed", "err", err)
		}
		c.db = db
	}

	if opts.EnableComponentRedis {
		client, err := redisx.InitRedis(&cfg.Redis)
		if err != nil {
			logger.S().Fatalw("init redis failed", "err", err)
		}
		c.redis = client
	}

	if opts.EnableComponentMQ {
		client, err := mq.NewClient(&cfg.MQ)
		if err != nil {
			logger.S().Fatalw("init mq failed", "err", err)
		}
		c.mq = client
	}

	return c
}

// DB implements Components.
func (c *components) DB() DBComponent {
	if c.db == nil {
		return nil
	}
	return &dbComponent{db: c.db}
}

// Redis implements Components.
func (c *components) Redis() RedisComponent {
	if c.redis == nil {
		return nil
	}
	return &redisComponent{client: c.redis}
}

// MQ implements Components.
func (c *components) MQ() MQComponent {
	if c.mq == nil {
		return nil
	}
	return &mqComponent{client: c.mq}
}

// Close releases all component resources.
func (c *components) Close() {
	if c.redis != nil {
		if err := c.redis.Close(); err != nil {
			logger.S().Warnw("close redis failed", "err", err)
		}
	}
	if c.mq != nil {
		if err := c.mq.Close(); err != nil {
			logger.S().Warnw("close mq failed", "err", err)
		}
	}
	if c.db != nil {
		if sqlDB, err := c.db.DB(); err == nil {
			if err := sqlDB.Close(); err != nil {
				logger.S().Warnw("close db failed", "err", err)
			}
		}
	}
}

type dbComponent struct {
	db *gorm.DB
}

func (d *dbComponent) GormDB() any { return d.db }

type redisComponent struct {
	client *goredis.Client
}

func (r *redisComponent) Client() any { return r.client }

type mqComponent struct {
	client mq.Client
}

func (m *mqComponent) Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error {
	return m.client.Publish(ctx, subject, body, headers)
}
