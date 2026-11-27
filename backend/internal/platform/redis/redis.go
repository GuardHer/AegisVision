package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Stream token kinds matching docs/design/02-protocols-and-apis.md section 6.2.
const (
	StreamTokenKindPlay = "play"
	StreamTokenKindPull = "pull"

	KeySchedulerLeader      = "scheduler:leader"
	ChannelAlarmsRealtime   = "alarms:realtime"
	ChannelAuthScopeChanged = "auth:scope_changed"
	FrameChannelPrefix      = "frames:"
	AgentRouteKeyPrefix     = "agent:route:"
	AuthBlacklistKeyPrefix  = "auth:blacklist:"
	UserScopeKeyPrefix      = "user:scope:"
)

// UserScopeCachePayload caches a user's resolved workshop/camera access scope in Redis.
type UserScopeCachePayload struct {
	Unrestricted     bool     `json:"unrestricted"`
	AllowedZoneIDs   []string `json:"allowed_zone_ids"`
	AllowedCameraIDs []string `json:"allowed_camera_ids"`
}

// ErrKeyNotFound indicates the requested Redis key does not exist or has expired.
var ErrKeyNotFound = errors.New("redis: key not found")

// StreamTokenPayload stores authorization metadata for MediaMTX play/pull tokens.
type StreamTokenPayload struct {
	UserID     string `json:"user_id,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	StreamPath string `json:"stream_path"`
}

// FrameMessage carries a realtime detection frame payload from Redis Pub/Sub.
type FrameMessage struct {
	CameraID string
	Payload  []byte
}

var (
	renewLeaderScript = goredis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)
	releaseLeaderScript = goredis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)
)

// Client wraps go-redis with AegisVision's hot-state, leader lock, and Pub/Sub primitives.
type Client struct {
	rdb *goredis.Client
}

// New connects to Redis and validates reachability via PING.
func New(ctx context.Context, addr, password string, db int) (*Client, error) {
	rdb := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis %q: %w", addr, err)
	}
	return &Client{rdb: rdb}, nil
}

// Close closes the underlying Redis connection pool.
func (c *Client) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// Ping verifies Redis connectivity.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

func streamTokenKey(kind, token string) string {
	return fmt.Sprintf("stream:%s:%s", kind, token)
}

// SetStreamToken stores a short-lived MediaMTX play or pull authorization token.
func (c *Client) SetStreamToken(ctx context.Context, kind, token string, payload StreamTokenPayload, ttl time.Duration) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal stream token payload: %w", err)
	}
	return c.rdb.Set(ctx, streamTokenKey(kind, token), raw, ttl).Err()
}

// GetStreamToken looks up a MediaMTX play or pull authorization token.
func (c *Client) GetStreamToken(ctx context.Context, kind, token string) (*StreamTokenPayload, error) {
	raw, err := c.rdb.Get(ctx, streamTokenKey(kind, token)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, ErrKeyNotFound
		}
		return nil, err
	}
	var payload StreamTokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal stream token payload: %w", err)
	}
	return &payload, nil
}

// AcquireSchedulerLeader attempts to acquire the global scheduler leader lock.
func (c *Client) AcquireSchedulerLeader(ctx context.Context, instanceID string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, KeySchedulerLeader, instanceID, ttl).Result()
}

// RenewSchedulerLeader extends the leader lock TTL only if still owned by instanceID.
func (c *Client) RenewSchedulerLeader(ctx context.Context, instanceID string, ttl time.Duration) (bool, error) {
	res, err := renewLeaderScript.Run(ctx, c.rdb, []string{KeySchedulerLeader}, instanceID, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

// ReleaseSchedulerLeader releases the leader lock only if owned by instanceID.
func (c *Client) ReleaseSchedulerLeader(ctx context.Context, instanceID string) error {
	_, err := releaseLeaderScript.Run(ctx, c.rdb, []string{KeySchedulerLeader}, instanceID).Result()
	return err
}

// SetAgentRoute records which aegis-ingest instance currently holds nodeID's gRPC stream.
func (c *Client) SetAgentRoute(ctx context.Context, nodeID, ingestInstanceID string, ttl time.Duration) error {
	return c.rdb.Set(ctx, AgentRouteKeyPrefix+nodeID, ingestInstanceID, ttl).Err()
}

// GetAgentRoute retrieves the aegis-ingest instance ID holding nodeID's stream.
func (c *Client) GetAgentRoute(ctx context.Context, nodeID string) (string, error) {
	val, err := c.rdb.Get(ctx, AgentRouteKeyPrefix+nodeID).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "", ErrKeyNotFound
		}
		return "", err
	}
	return val, nil
}

// BlacklistToken records a revoked JWT jti until its natural expiration.
func (c *Client) BlacklistToken(ctx context.Context, jti string, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	return c.rdb.Set(ctx, AuthBlacklistKeyPrefix+jti, "1", ttl).Err()
}

// IsTokenBlacklisted checks whether a JWT jti has been revoked.
func (c *Client) IsTokenBlacklisted(ctx context.Context, jti string) (bool, error) {
	n, err := c.rdb.Exists(ctx, AuthBlacklistKeyPrefix+jti).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// PublishFrame publishes a transient realtime detection frame for cameraID.
func (c *Client) PublishFrame(ctx context.Context, cameraID string, payload []byte) error {
	return c.rdb.Publish(ctx, FrameChannelPrefix+cameraID, payload).Err()
}

// SubscribeFrames subscribes to realtime detection frames for specific cameras (or all cameras if none passed).
func (c *Client) SubscribeFrames(ctx context.Context, cameraIDs ...string) (<-chan FrameMessage, func() error, error) {
	var pubsub *goredis.PubSub
	if len(cameraIDs) == 0 {
		pubsub = c.rdb.PSubscribe(ctx, FrameChannelPrefix+"*")
	} else {
		channels := make([]string, len(cameraIDs))
		for i, id := range cameraIDs {
			channels[i] = FrameChannelPrefix + id
		}
		pubsub = c.rdb.Subscribe(ctx, channels...)
	}

	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, fmt.Errorf("subscribe frames: %w", err)
	}

	out := make(chan FrameMessage, 256)
	go func() {
		defer close(out)
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				camID := strings.TrimPrefix(msg.Channel, FrameChannelPrefix)
				frame := FrameMessage{
					CameraID: camID,
					Payload:  []byte(msg.Payload),
				}
				select {
				case out <- frame:
				default:
					// Drop oldest on consumer lag to preserve realtime freshness.
					select {
					case <-out:
					default:
					}
					select {
					case out <- frame:
					default:
					}
				}
			}
		}
	}()

	return out, pubsub.Close, nil
}

// PublishRealtimeAlarm broadcasts a global realtime alarm alert to aegis-ws instances.
func (c *Client) PublishRealtimeAlarm(ctx context.Context, payload []byte) error {
	return c.rdb.Publish(ctx, ChannelAlarmsRealtime, payload).Err()
}

// SubscribeRealtimeAlarms subscribes to global realtime alarm alerts.
func (c *Client) SubscribeRealtimeAlarms(ctx context.Context) (<-chan []byte, func() error, error) {
	pubsub := c.rdb.Subscribe(ctx, ChannelAlarmsRealtime)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, fmt.Errorf("subscribe realtime alarms: %w", err)
	}

	out := make(chan []byte, 128)
	go func() {
		defer close(out)
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- []byte(msg.Payload):
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, pubsub.Close, nil
}

// SetUserScopeCache stores a user's resolved workshop/camera scope in Redis.
func (c *Client) SetUserScopeCache(ctx context.Context, userID string, payload UserScopeCachePayload, ttl time.Duration) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal user scope cache: %w", err)
	}
	return c.rdb.Set(ctx, UserScopeKeyPrefix+userID, raw, ttl).Err()
}

// GetUserScopeCache fetches a cached user scope payload from Redis.
func (c *Client) GetUserScopeCache(ctx context.Context, userID string) (*UserScopeCachePayload, error) {
	raw, err := c.rdb.Get(ctx, UserScopeKeyPrefix+userID).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, ErrKeyNotFound
		}
		return nil, err
	}
	var payload UserScopeCachePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal user scope cache: %w", err)
	}
	return &payload, nil
}

// DeleteUserScopeCache evicts a user's cached workshop scope immediately upon permission update.
func (c *Client) DeleteUserScopeCache(ctx context.Context, userID string) error {
	return c.rdb.Del(ctx, UserScopeKeyPrefix+userID).Err()
}

// DeleteAllUserScopeCaches evicts all cached user workshop scopes after camera/zone topology changes.
func (c *Client) DeleteAllUserScopeCaches(ctx context.Context) error {
	var cursor uint64
	for {
		keys, next, err := c.rdb.Scan(ctx, cursor, UserScopeKeyPrefix+"*", 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if delErr := c.rdb.Del(ctx, keys...).Err(); delErr != nil {
				return delErr
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}

// PublishScopeChanged notifies aegis-ws instances that userID's workshop scope changed.
func (c *Client) PublishScopeChanged(ctx context.Context, userID string) error {
	return c.rdb.Publish(ctx, ChannelAuthScopeChanged, userID).Err()
}

// SubscribeScopeChanged listens for workshop scope revocation/update events across aegis-ws replicas.
func (c *Client) SubscribeScopeChanged(ctx context.Context) (<-chan string, func() error, error) {
	pubsub := c.rdb.Subscribe(ctx, ChannelAuthScopeChanged)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, fmt.Errorf("subscribe auth scope changed: %w", err)
	}

	out := make(chan string, 64)
	go func() {
		defer close(out)
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				uid := strings.TrimSpace(msg.Payload)
				if uid == "" {
					continue
				}
				select {
				case out <- uid:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, pubsub.Close, nil
}
