package store

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisStore struct{ client *redis.Client }

func NewRedis(rawURL string) (*RedisStore, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &RedisStore{client: redis.NewClient(opt)}, nil
}
func (s *RedisStore) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }
func (s *RedisStore) Close() error                   { return s.client.Close() }
func (s *RedisStore) Save(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	return s.client.Set(ctx, "kubepilot:checkpoint:"+key, data, ttl).Err()
}
func (s *RedisStore) Load(ctx context.Context, key string) ([]byte, error) {
	b, err := s.client.Get(ctx, "kubepilot:checkpoint:"+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	return b, err
}
func (s *RedisStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, "kubepilot:checkpoint:"+key).Err()
}
func (s *RedisStore) SaveEinoCheckpoint(ctx context.Context, id string, data []byte, ttl time.Duration) error {
	return s.client.Set(ctx, "kubepilot:eino:checkpoint:"+id, data, ttl).Err()
}
func (s *RedisStore) LoadEinoCheckpoint(ctx context.Context, id string) ([]byte, error) {
	b, err := s.client.Get(ctx, "kubepilot:eino:checkpoint:"+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	return b, err
}
func (s *RedisStore) DeleteEinoCheckpoint(ctx context.Context, id string) error {
	return s.client.Del(ctx, "kubepilot:eino:checkpoint:"+id).Err()
}
func (s *RedisStore) Lock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, "kubepilot:lock:"+key, owner, ttl).Result()
}
func (s *RedisStore) Unlock(ctx context.Context, key, owner string) error {
	script := redis.NewScript(`if redis.call("get",KEYS[1])==ARGV[1] then return redis.call("del",KEYS[1]) else return 0 end`)
	return script.Run(ctx, s.client, []string{"kubepilot:lock:" + key}, owner).Err()
}
func (s *RedisStore) RefreshLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	script := redis.NewScript(`if redis.call("get",KEYS[1])==ARGV[1] then return redis.call("pexpire",KEYS[1],ARGV[2]) else return 0 end`)
	result, err := script.Run(ctx, s.client, []string{"kubepilot:lock:" + key}, owner, ttl.Milliseconds()).Int64()
	return result == 1, err
}

func (s *RedisStore) SaveCursor(ctx context.Context, key string, value time.Time) error {
	return s.client.Set(ctx, "kubepilot:cursor:"+key, value.UTC().Format(time.RFC3339Nano), 0).Err()
}

func (s *RedisStore) LoadCursor(ctx context.Context, key string) (time.Time, error) {
	value, err := s.client.Get(ctx, "kubepilot:cursor:"+key).Result()
	if errors.Is(err, redis.Nil) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, value)
}

func (s *RedisStore) ClaimAction(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, "kubepilot:action:"+key, "running", ttl).Result()
}

func (s *RedisStore) CompleteAction(ctx context.Context, key, status string, ttl time.Duration) error {
	script := redis.NewScript(`if redis.call("exists",KEYS[1])==1 then redis.call("set",KEYS[1],ARGV[1],"PX",ARGV[2]); return 1 else return 0 end`)
	return script.Run(ctx, s.client, []string{"kubepilot:action:" + key}, status, ttl.Milliseconds()).Err()
}
