package zdelta

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

// The testdata was written by the zstd CLI, which is libzstd, the library the
// Rust client links. `next.json.patch.zst` is `zstd --patch-from=base.json`.
func TestDecodeReadsLibzstdOutput(t *testing.T) {
	base, next := fixture(t, "base.json"), fixture(t, "next.json")

	plain, err := Decode(fixture(t, "next.json.zst"), nil, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, next, plain)

	patched, err := Decode(fixture(t, "next.json.patch.zst"), base, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, next, patched)
}

func TestEncodeRoundTripsAndShrinksAgainstABase(t *testing.T) {
	base, next := fixture(t, "base.json"), fixture(t, "next.json")

	full, err := Encode(next, nil)
	require.NoError(t, err)
	delta, err := Encode(next, base)
	require.NoError(t, err)

	got, err := Decode(delta, base, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, next, got)
	got, err = Decode(full, nil, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, next, got)

	assert.Less(t, len(delta)*10, len(full), "a delta must be far smaller than the plain frame: delta %d, full %d", len(delta), len(full))
}

func TestDecodeRejectsTheWrongBase(t *testing.T) {
	base, next := fixture(t, "base.json"), fixture(t, "next.json")
	delta, err := Encode(next, base)
	require.NoError(t, err)

	got, err := Decode(delta, []byte("not the base"), 1<<20)
	if err == nil {
		assert.NotEqual(t, next, got)
	}
	_, err = Decode(delta, nil, 1<<20)
	assert.Error(t, err)
}

func TestDecodeEnforcesTheSizeLimit(t *testing.T) {
	next := fixture(t, "next.json")
	frame, err := Encode(next, nil)
	require.NoError(t, err)

	_, err = Decode(frame, nil, int64(len(next)-1))
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestWindowFor(t *testing.T) {
	assert.Equal(t, 1024, windowFor(1))
	assert.Equal(t, 1<<18, windowFor(1<<18))
	assert.Equal(t, 1<<19, windowFor(1<<18+1))
}
