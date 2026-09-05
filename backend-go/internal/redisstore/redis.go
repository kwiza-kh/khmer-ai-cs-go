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

// CheckRateLimit implements the fixed window: INCR on the key, first hit sets
// a 60s expiry; count over max means "blocked".
func (c *Client) CheckRateLimit(ctx context.Context, key string, max uint32) (bool, error) {
	rk := "ratelimit:" + key
	count, err := c.rdb.Incr(ctx, rk).Result()
	if err != nil {
		return false, err
	}
	if count == 1 {
		if err := c.rdb.Expire(ctx, rk, time.Minute).Err(); err != nil {
			return false, err
		}
	}
	return uint32(count) <= max, nil
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
