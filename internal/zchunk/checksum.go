package zchunk

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
)

// Sum computes the checksum of data under the receiver's algorithm, truncated to
// the type's digest size. Per the reference implementation, SHA-512/128 is plain
// SHA-512 truncated to its first 16 bytes (not a distinct-IV variant). It
// returns an error for an unknown checksum type.
func (t ChecksumType) Sum(data []byte) ([]byte, error) {
	size, err := t.Size()
	if err != nil {
		return nil, err
	}
	var full []byte
	switch t {
	case SHA1:
		s := sha1.Sum(data)
		full = s[:]
	case SHA256:
		s := sha256.Sum256(data)
		full = s[:]
	case SHA512, SHA512128:
		s := sha512.Sum512(data)
		full = s[:]
	}
	return full[:size], nil
}
