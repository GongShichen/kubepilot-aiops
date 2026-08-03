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
func (s *RedisStore) Lock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return s.client.SetNX(ctx, "kubepilot:lock:"+key, owner, ttl).Result()
}
func (s *RedisStore) Unlock(ctx context.Context, key, owner string) error {
	script := redis.NewScript(`if redis.call("get",KEYS[1])==ARGV[1] then return redis.call("del",KEYS[1]) else return 0 end`)
	return script.Run(ctx, s.client, []string{"kubepilot:lock:" + key}, owner).Err()
}
