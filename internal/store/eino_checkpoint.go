package store

import (
	"context"
	"errors"
	"time"
)

// EinoCheckpointStore adapts RedisStore to Eino's checkpoint contract.
type EinoCheckpointStore struct {
	Redis *RedisStore
	TTL   time.Duration
}

func (s EinoCheckpointStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	data, err := s.Redis.LoadEinoCheckpoint(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	return data, err == nil, err
}
func (s EinoCheckpointStore) Set(ctx context.Context, id string, data []byte) error {
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return s.Redis.SaveEinoCheckpoint(ctx, id, data, ttl)
}
func (s EinoCheckpointStore) Delete(ctx context.Context, id string) error {
	return s.Redis.DeleteEinoCheckpoint(ctx, id)
}
