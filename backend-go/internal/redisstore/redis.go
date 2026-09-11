// Package redisstore wraps the Redis primitives the app uses: fixed-window
// rate limiting, the sliding-window session context, generic JSON storage and
// pub/sub (realtime inbox fan-out). Semantics mirror the Rust client.
package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	rdb *redis.Client
}

// Connect dials addr (host:port), selects db, and verifies with PING.
func Connect(addr, password string, db int) (*Client, error) {
	host, port := addr, "6379"
	if h, p, ok := splitHostPort(addr); ok {
		host, port = h, p
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     host + ":" + port,
		Password: password,
		DB:       db,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connect failed: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

func splitHostPort(addr string) (string, string, bool) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], true
		}
	}
	return "", "", false
}

// Ping reports reachability (readiness probe).
func (c *Client) Ping(ctx context.Context) bool {
	return c.rdb.Ping(ctx).Err() == nil
}

// CheckRateLimit implements the fixed window: the key is created atomically
// with its 60s TTL (SET NX), then INCR'd — count over max means "blocked".
// The old INCR-then-EXPIRE pattern could leak a TTL-less key on a crash and
// block the caller permanently.
func (c *Client) CheckRateLimit(ctx context.Context, key string, max uint32) (bool, error) {
	rk := "ratelimit:" + key
	if err := c.rdb.SetNX(ctx, rk, 0, time.Minute).Err(); err != nil {
		return false, err
	}
	// Self-heal poison keys: a window left without a TTL (leaked by the old
	// INCR-then-EXPIRE pattern) never expires, and SET NX is a no-op on an
	// existing key — its owner stays rate-limited forever once the count
	// passes max. Re-asserting the TTL on every check bounds such a key to
	// one extra window.
	if err := c.rdb.Expire(ctx, rk, time.Minute).Err(); err != nil {
		return false, err
	}
	count, err := c.rdb.Incr(ctx, rk).Result()
	if err != nil {
		return false, err
	}
	return uint32(count) <= max, nil
}

// IncrWindow — fixed-window counter with a custom expiry (e.g. daily or
// hourly caps). Reports whether the incremented count stays within max.
// Key creation and TTL are atomic (SET NX) — no TTL-less leak on crash.
func (c *Client) IncrWindow(ctx context.Context, key string, max int64, ttl time.Duration) (bool, error) {
	rk := "ratelimit:" + key
	if err := c.rdb.SetNX(ctx, rk, 0, ttl).Err(); err != nil {
		return false, err
	}
	// Same poison-key self-heal as CheckRateLimit: re-assert the window TTL.
	if err := c.rdb.Expire(ctx, rk, ttl).Err(); err != nil {
		return false, err
	}
	count, err := c.rdb.Incr(ctx, rk).Result()
	if err != nil {
		return false, err
	}
	return count <= max, nil
}

// GetString — raw string read ("" and nil error when the key is missing).
func (c *Client) GetString(ctx context.Context, key string) (string, error) {
	return c.rdb.Get(ctx, key).Result()
}

// SetString — raw string write with TTL.
func (c *Client) SetString(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

// Del — remove keys (used to consume one-time tokens).
func (c *Client) Del(ctx context.Context, keys ...string) error {
	return c.rdb.Del(ctx, keys...).Err()
}

// PushWindow appends one JSON message onto the sliding window (RPush + trim to
// the newest `window` entries + refresh TTL).
func (c *Client) PushWindow(ctx context.Context, key string, v any, window int64, ttl time.Duration) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := c.rdb.RPush(ctx, key, payload).Err(); err != nil {
		return err
	}
	if err := c.rdb.LTrim(ctx, key, -window, -1).Err(); err != nil {
		return err
	}
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// GetWindow reads the sliding window (oldest first), skipping malformed items.
func (c *Client) GetWindow(ctx context.Context, key string) ([]json.RawMessage, error) {
	items, err := c.rdb.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if json.Valid([]byte(item)) {
			out = append(out, json.RawMessage(item))
		}
	}
	return out, nil
}

// SetJSON stores a JSON value with TTL.
func (c *Client) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, payload, ttl).Err()
}

// GetJSON fetches a JSON value; (nil, nil) when missing.
func (c *Client) GetJSON(ctx context.Context, key string) (json.RawMessage, error) {
	item, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid(item) {
		return nil, nil
	}
	return json.RawMessage(item), nil
}

// Publish sends a payload on a channel (realtime fan-out between instances).
func (c *Client) Publish(ctx context.Context, channel, payload string) error {
	return c.rdb.Publish(ctx, channel, payload).Err()
}

// Subscribe opens a pub/sub subscription (realtime fan-in between instances).
// The initial subscription is verified before return; the caller owns Close.
func (c *Client) Subscribe(ctx context.Context, channels ...string) (*redis.PubSub, error) {
	ps := c.rdb.Subscribe(ctx, channels...)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, err
	}
	return ps, nil
}
