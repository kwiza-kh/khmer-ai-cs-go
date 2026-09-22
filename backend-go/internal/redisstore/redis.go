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

// incrWindowScript increments a fixed-window counter and manages its TTL in a
// single atomic step:
//
//	count == 1        → brand-new window, arm the TTL
//	TTL  < 0          → poison key (no expiry, leaked by the pre-2026-09
//	                    INCR-then-EXPIRE pattern), arm the TTL to self-heal
//	otherwise         → leave the TTL alone
//
// That last branch is the whole point. Re-arming the TTL on *every* call — as
// an earlier "self-heal" fix did — means the window never closes for a caller
// that keeps sending: the count only grows, so once it passes max the caller
// is rate-limited for as long as it keeps retrying, and every retry pushes the
// deadline out another full window. Fixed windows must expire on schedule.
//
// Doing it in Lua keeps the check-and-arm atomic, so two concurrent requests
// on a fresh key can't both observe count==1 and race on the TTL.
var incrWindowScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
elseif redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// incrWindow returns the post-increment count for key, arming ttl only when
// the window is new or the key had lost its expiry.
func (c *Client) incrWindow(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrWindowScript.Run(ctx, c.rdb, []string{key}, ttl.Milliseconds()).Int64()
}

// IncrCounter atomically increments key and arms ttl when the counter is new
// (or had lost its expiry). Unlike the rate-limit helpers it adds no key
// prefix, so it can back counters such as the login-failure tally where the
// read-modify-write pattern used to lose increments under concurrency.
func (c *Client) IncrCounter(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrWindowScript.Run(ctx, c.rdb, []string{key}, ttl.Milliseconds()).Int64()
}

// CheckRateLimit implements the 60s fixed window: count over max means
// "blocked". Returns the caller's count within the current window.
func (c *Client) CheckRateLimit(ctx context.Context, key string, max uint32) (bool, error) {
	count, err := c.incrWindow(ctx, "ratelimit:"+key, time.Minute)
	if err != nil {
		return false, err
	}
	return uint32(count) <= max, nil
}

// IncrWindow — fixed-window counter with a custom expiry (e.g. daily or
// hourly caps). Reports whether the incremented count stays within max.
func (c *Client) IncrWindow(ctx context.Context, key string, max int64, ttl time.Duration) (bool, error) {
	count, err := c.incrWindow(ctx, "ratelimit:"+key, ttl)
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
