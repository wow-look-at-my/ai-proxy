package zdelta

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStoreEvictsLeastRecentlyUsedWithinBudget(t *testing.T) {
	s := NewStore(30)
	a := s.Put(bytes.Repeat([]byte("a"), 10))
	b := s.Put(bytes.Repeat([]byte("b"), 10))
	_, ok := s.Get(a)
	assert.True(t, ok)
	c := s.Put(bytes.Repeat([]byte("c"), 10))
	d := s.Put(bytes.Repeat([]byte("d"), 10))

	_, ok = s.Get(b)
	assert.False(t, ok, "b was the least recently used")
	for _, h := range []string{a, c, d} {
		_, ok = s.Get(h)
		assert.True(t, ok)
	}
	assert.Equal(t, 3, s.Len())
}

func TestStoreSkipsABodyLargerThanTheBudget(t *testing.T) {
	s := NewStore(5)
	h := s.Put([]byte("too large"))
	_, ok := s.Get(h)
	assert.False(t, ok)
	assert.Equal(t, 0, s.Len())
}

func TestStorePutIsIdempotent(t *testing.T) {
	s := NewStore(100)
	h1 := s.Put([]byte("same"))
	h2 := s.Put([]byte("same"))
	assert.Equal(t, h1, h2)
	assert.Equal(t, 1, s.Len())
	assert.Equal(t, Hash([]byte("same")), h1)
}
