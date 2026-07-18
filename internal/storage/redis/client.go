package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisClientInterface is the companion interface for RedisClient.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type RedisClientInterface interface {
	Ping(ctx context.Context) error
	KeyBuilder() any
	Incr(ctx context.Context, key string) (int64, error)
	Decr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, expiration int64) error
	Del(ctx context.Context, key string) error
	Scan(ctx context.Context, cursor uint64, match string, count int64) (keys []string, nextCursor uint64, err error)
	IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error)
	AddWithExpire(ctx context.Context, key string, delta int64, expireSeconds int64) (int64, error)
}

// RedisClient wraps a Redis client with mandatory key-prefix enforcement.
// Services should never have access to raw Set/Get/Del operations - they must
// use wrapper methods that accept (ctx, profileID, category, identifier, ...).
type RedisClient struct {
	client     *redis.Client
	keyBuilder KeyBuilderInterface
}

// Ensure RedisClient implements RedisClientInterface
var _ RedisClientInterface = (*RedisClient)(nil)

// ConfigProvider defines the configuration needed for Redis connection.
type ConfigProvider interface {
	GetRedisAddr() string
	GetRedisPassword() string
	GetRedisDB() int
}

type tlsConfigProvider interface {
	GetRedisTLSEnabled() bool
}

// NewClient creates a new Redis client wrapper.
// It establishes the connection and pings the server to verify connectivity.
func NewClient(ctx context.Context, cfg ConfigProvider) (*RedisClient, error) {
	client := redis.NewClient(optionsFromConfig(cfg))

	// Verify connection with ping
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to redis: %w", err)
	}

	return &RedisClient{
		client:     client,
		keyBuilder: NewKeyBuilder(),
	}, nil
}

func optionsFromConfig(cfg ConfigProvider) *redis.Options {
	options := &redis.Options{
		Addr:     cfg.GetRedisAddr(),
		Password: cfg.GetRedisPassword(),
		DB:       cfg.GetRedisDB(),
	}
	if tlsCfg, ok := cfg.(tlsConfigProvider); ok && tlsCfg.GetRedisTLSEnabled() {
		options.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return options
}

// Ping verifies the Redis connection is healthy.
func (rc *RedisClient) Ping(ctx context.Context) error {
	return rc.client.Ping(ctx).Err()
}

// KeyBuilder returns the KeyBuilderInterface for constructing prefixed keys.
func (rc *RedisClient) KeyBuilder() any {
	return rc.keyBuilder
}

// GetClient returns the underlying redis.Client for internal use.
// This should only be used by the wrapper methods within this package.
func (rc *RedisClient) GetClient() *redis.Client {
	return rc.client
}

// Close closes the Redis connection.
func (rc *RedisClient) Close() error {
	return rc.client.Close()
}

// Incr increments the value stored at key by one.
// Returns the new value after increment.
func (rc *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	return rc.client.Incr(ctx, key).Result()
}

// Decr decrements the value stored at key by one.
// Returns the new value after decrement.
func (rc *RedisClient) Decr(ctx context.Context, key string) (int64, error) {
	return rc.client.Decr(ctx, key).Result()
}

// Expire sets an expiration time on a key.
// expiration is in seconds.
func (rc *RedisClient) Expire(ctx context.Context, key string, expiration int64) error {
	return rc.client.Expire(ctx, key, time.Duration(expiration)*time.Second).Err()
}

// Del deletes a key.
func (rc *RedisClient) Del(ctx context.Context, key string) error {
	return rc.client.Del(ctx, key).Err()
}

// Scan iterates over keys matching a pattern.
// Returns keys found and the next cursor for pagination.
func (rc *RedisClient) Scan(ctx context.Context, cursor uint64, match string, count int64) (keys []string, nextCursor uint64, err error) {
	return rc.client.Scan(ctx, cursor, match, count).Result()
}

// IncrWithExpire increments the key and sets expiration if this is the first increment.
// This is atomic and avoids race conditions in the check-then-set pattern.
// Returns the new value after increment.
func (rc *RedisClient) IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error) {
	// Use a Lua script for atomic increment + conditional expire
	script := `
		local current = redis.call('INCR', KEYS[1])
		if current == 1 then
			redis.call('EXPIRE', KEYS[1], ARGV[1])
		end
		return current
	`
	result, err := rc.client.Eval(ctx, script, []string{key}, expireSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("failed to increment with expire: %w", err)
	}
	return result, nil
}

// AddWithExpire adds delta to the key and sets expiration if this is the first write.
// This is atomic and is used for quota counters where one operation can consume
// more than one unit, such as stored content bytes.
func (rc *RedisClient) AddWithExpire(ctx context.Context, key string, delta int64, expireSeconds int64) (int64, error) {
	script := `
		local delta = tonumber(ARGV[1])
		local current = redis.call('INCRBY', KEYS[1], delta)
		if current == delta then
			redis.call('EXPIRE', KEYS[1], ARGV[2])
		end
		return current
	`
	result, err := rc.client.Eval(ctx, script, []string{key}, delta, expireSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("failed to add with expire: %w", err)
	}
	return result, nil
}
