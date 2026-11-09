package zdelta

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStoreEvictsLeastRecentlyUsedWithinBudget(t *testing.T) {
	s := NewStore(30)
	a := s.Put("k", bytes.Repeat([]byte("a"), 10))
	b := s.Put("k", bytes.Repeat([]byte("b"), 10))
	_, ok := s.Get("k", a)
	assert.True(t, ok)
	c := s.Put("k", bytes.Repeat([]byte("c"), 10))
	d := s.Put("k", bytes.Repeat([]byte("d"), 10))

	_, ok = s.Get("k", b)
	assert.False(t, ok, "b was the least recently used")
	for _, h := range []string{a, c, d} {
		_, ok = s.Get("k", h)
		assert.True(t, ok)
	}
	assert.Equal(t, 3, s.Len())
}

func TestStoreSkipsABodyLargerThanTheBudget(t *testing.T) {
	s := NewStore(5)
	h := s.Put("k", []byte("too large"))
	_, ok := s.Get("k", h)
	assert.False(t, ok)
	assert.Equal(t, 0, s.Len())
}

func TestStorePutIsIdempotent(t *testing.T) {
	s := NewStore(100)
	h1 := s.Put("k", []byte("same"))
	h2 := s.Put("k", []byte("same"))
	assert.Equal(t, h1, h2)
	assert.Equal(t, 1, s.Len())
	assert.Equal(t, Hash([]byte("same")), h1)
}

func TestStoreFindsABodyOnlyUnderItsScope(t *testing.T) {
	s := NewStore(100)
	h := s.Put("alice", []byte("alice's history"))
	_, ok := s.Get("mallory", h)
	assert.False(t, ok, "another scope must not see the body, even with its hash")
	got, ok := s.Get("alice", h)
	assert.True(t, ok)
	assert.Equal(t, []byte("alice's history"), got)
}
