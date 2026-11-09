// codec.go holds AcceptEncodingValue.

package zdelta

import (
	"errors"
	"fmt"
	"math/bits"

	"github.com/klauspost/compress/zstd"
)

// Wire names. A relay sends AcceptEncodingValue in Accept-Encoding (RFC 7694)
// and DeltaValue in DeltaHeader on every response.
const (
	AcceptEncodingValue = "zstd"
	DeltaHeader         = "X-Zstd-Delta"
	DeltaValue          = "sha256"
	BaseHeader          = "X-Zstd-Base"
	BaseMissingHeader   = "X-Zstd-Base-Missing"
)

// ErrTooLarge means a decoded body passed the size limit.
var ErrTooLarge = errors.New("decoded body exceeds the size limit")

// Decode decompresses one zstd body. A non-nil base is the raw dictionary the
// client encoded against, as `zstd --patch-from` and a libzstd reference
// prefix do. The result is at most maxSize bytes.
func Decode(body, base []byte, maxSize int64) ([]byte, error) {
	opts := []zstd.DOption{
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(uint64(maxSize)),
		// A delta frame's window spans the base and the body, so it may reach twice maxSize.
		zstd.WithDecoderMaxWindow(uint64(max(2*maxSize, zstd.MinWindowSize))),
	}
	if base != nil {
		opts = append(opts, zstd.WithDecoderDictRaw(0, base))
	}
	dec, err := zstd.NewReader(nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("zstd decoder: %w", err)
	}
	defer dec.Close()
	out, err := dec.DecodeAll(body, nil)
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) || errors.Is(err, zstd.ErrWindowSizeExceeded) {
		return nil, ErrTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("zstd decode: %w", err)
	}
	if int64(len(out)) > maxSize {
		return nil, ErrTooLarge
	}
	return out, nil
}

// Encode compresses plain for a relay. A non-nil base must be a body the relay
// already holds; the result then carries only what plain adds to it.
func Encode(plain, base []byte) ([]byte, error) {
	opts := []zstd.EOption{
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(windowFor(len(plain) + len(base))),
	}
	if base != nil {
		opts = append(opts, zstd.WithEncoderDictRaw(0, base))
	}
	enc, err := zstd.NewWriter(nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("zstd encoder: %w", err)
	}
	defer enc.Close()
	return enc.EncodeAll(plain, nil), nil
}

func windowFor(n int) int {
	if n <= zstd.MinWindowSize {
		return zstd.MinWindowSize
	}
	w := 1 << bits.Len(uint(n-1))
	return min(w, zstd.MaxWindowSize)
}
