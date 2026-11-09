// Package zdelta holds the request bodies a client may encode the next one
// against, and the zstd codec for those deltas.
package zdelta

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Hash is the key a client names a base with: the lowercase hex SHA-256 of
// the plain body.
func Hash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Store keeps recent bodies by hash, least recently used first out, within
// a total byte budget.
type Store struct {
	mu       sync.Mutex
	maxBytes int64
	size     int64
	order    *list.List
	byHash   map[string]*list.Element
}

type entry struct {
	hash string
	body []byte
}

// NewStore returns a store that holds at most maxBytes of bodies.
func NewStore(maxBytes int64) *Store {
	return &Store{maxBytes: maxBytes, order: list.New(), byHash: map[string]*list.Element{}}
}

// Put keeps body under its hash and returns the hash. A body larger than the
// whole budget is not kept.
func (s *Store) Put(body []byte) string {
	hash := Hash(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.byHash[hash]; ok {
		s.order.MoveToFront(el)
		return hash
	}
	if int64(len(body)) > s.maxBytes {
		return hash
	}
	s.byHash[hash] = s.order.PushFront(&entry{hash: hash, body: body})
	s.size += int64(len(body))
	for s.size > s.maxBytes {
		oldest := s.order.Back()
		e := oldest.Value.(*entry)
		s.order.Remove(oldest)
		delete(s.byHash, e.hash)
		s.size -= int64(len(e.body))
	}
	return hash
}

// Get returns the body kept under hash. The caller must not change it.
func (s *Store) Get(hash string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.byHash[hash]
	if !ok {
		return nil, false
	}
	s.order.MoveToFront(el)
	return el.Value.(*entry).body, true
}

// Len returns how many bodies the store holds.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}
