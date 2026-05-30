package zchunk

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
)

// newHash returns a fresh streaming hasher for the receiver's algorithm, or an
// error for an unknown checksum type. SHA-512/128 uses a full SHA-512 hasher;
// callers truncate the digest to the type's Size. It backs both the one-shot
// Sum and incremental hashing (e.g. verifying a body as it streams).
func (t ChecksumType) newHash() (hash.Hash, error) {
	switch t {
	case SHA1:
		return sha1.New(), nil
	case SHA256:
		return sha256.New(), nil
	case SHA512, SHA512128:
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("zchunk: unknown checksum type %d", uint64(t))
	}
}

// Sum computes the checksum of data under the receiver's algorithm, truncated to
// the type's digest size. Per the reference implementation, SHA-512/128 is plain
// SHA-512 truncated to its first 16 bytes (not a distinct-IV variant). It
// returns an error for an unknown checksum type.
func (t ChecksumType) Sum(data []byte) ([]byte, error) {
	size, err := t.Size()
	if err != nil {
		return nil, err
	}
	// Size validated the type, so newHash cannot fail here.
	h, _ := t.newHash()
	h.Write(data)
	return h.Sum(nil)[:size], nil
}
