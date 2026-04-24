package state

import "sync"

const numShards = 16

// ShardedMap is a goroutine-safe map[string]V split across N shards.
// Each shard has its own mutex, so operations on different keys contend
// only when they hash to the same shard — reducing lock pressure vs. a
// single global mutex.
type ShardedMap[V any] struct {
	shards [numShards]mapShard[V]
}

type mapShard[V any] struct {
	mu   sync.Mutex
	data map[string]V
}

// Use runs fn with the value for key, creating it first if absent.
// fn executes under the shard lock, so it is safe to mutate the value.
func (m *ShardedMap[V]) Use(key string, create func() V, fn func(V)) {
	s := &m.shards[fnv32(key)%numShards]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]V)
	}
	v, ok := s.data[key]
	if !ok {
		v = create()
		s.data[key] = v
	}
	fn(v)
}

// fnv32 is a fast non-cryptographic hash used for shard selection.
func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
