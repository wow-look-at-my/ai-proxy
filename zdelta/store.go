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

// Store keeps recent bodies by scope and hash, least recently used first out,
// within a total byte budget.
type Store struct {
	mu       sync.Mutex
	maxBytes int64
	size     int64
	order    *list.List
	byHash   map[string]*list.Element
}

type entry struct {
	key  string
	body []byte
}

func scopedKey(scope, hash string) string {
	return scope + "\x00" + hash
}

// NewStore returns a store that holds at most maxBytes of bodies.
func NewStore(maxBytes int64) *Store {
	return &Store{maxBytes: maxBytes, order: list.New(), byHash: map[string]*list.Element{}}
}

// Put keeps body under scope and its hash, and returns the hash. A body larger
// than the whole budget is not kept.
func (s *Store) Put(scope string, body []byte) string {
	hash := Hash(body)
	key := scopedKey(scope, hash)
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.byHash[key]; ok {
		s.order.MoveToFront(el)
		return hash
	}
	if int64(len(body)) > s.maxBytes {
		return hash
	}
	s.byHash[key] = s.order.PushFront(&entry{key: key, body: body})
	s.size += int64(len(body))
	for s.size > s.maxBytes {
		oldest := s.order.Back()
		e := oldest.Value.(*entry)
		s.order.Remove(oldest)
		delete(s.byHash, e.key)
		s.size -= int64(len(e.body))
	}
	return hash
}

// Get returns the body that scope put under hash. The caller must not change it.
func (s *Store) Get(scope, hash string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.byHash[scopedKey(scope, hash)]
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
