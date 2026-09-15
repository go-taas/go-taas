// Package redisx provides the Redis connection component. The platform
// targets Redis-compatible servers, including Valkey.
package redisx

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
)

// InitRedis opens the Redis connection described by cfg and pings it to
// fail fast on unreachable servers.
func InitRedis(cfg *config.Redis) (*goredis.Client, error) {
	client := goredis.NewClient(&goredis.Options{
		Addr:            fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password:        cfg.Password,
		DB:              cfg.DB,
		MinIdleConns:    cfg.MaxIdle,
		PoolSize:        cfg.MaxActive,
		ConnMaxIdleTime: cfg.IdleTimeout,
	})

	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisx: ping %s:%d: %w", cfg.Host, cfg.Port, err)
	}

	logger.S().Infow("redis connected", "host", cfg.Host, "port", cfg.Port, "db", cfg.DB)
	return client, nil
}
