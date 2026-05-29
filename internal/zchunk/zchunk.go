// Package zchunk implements primitives of the zchunk file format — a
// content-defined-chunked container (as used by Fedora's DNF/librepo) that
// supports delta downloads: a client fetches only the chunks it is missing
// over HTTP range requests, recovering the rest from a local copy.
//
// This package currently provides the format's foundational building blocks:
// the file magic, the variable-length "compressed integer" codec and the lead
// (the fixed prologue that lets a client validate the header). The preface,
// chunk index and zstd handling land here incrementally — every addition under
// the org's 100%-coverage rule.
//
// The binary layout follows the canonical zchunk_format.txt from the reference
// C implementation (github.com/zchunk/zchunk).
package zchunk

import (
	"errors"
	"fmt"
	"io"
)

// Magic is the 5-byte lead ID of a zchunk version 1 file: a NUL byte followed
// by the ASCII bytes "ZCK1".
const Magic = "\x00ZCK1"

// DetachedMagic is the lead ID of a detached zchunk version 1 header ("\0ZHR1").
// When validating a detached header's checksum, libraries substitute Magic for
// this ID so the digest matches the embedded form.
const DetachedMagic = "\x00ZHR1"

// MaxCompressedIntLen is the largest number of bytes a uint64 can occupy when
// encoded as a zchunk compressed integer: ceil(64/7) = 10 groups of 7 bits.
const MaxCompressedIntLen = 10

var (
	// ErrOverflow is returned when a compressed integer does not fit in uint64.
	ErrOverflow = errors.New("zchunk: compressed integer overflows uint64")
	// ErrTruncated is returned when the input ends in the middle of an integer.
	ErrTruncated = errors.New("zchunk: truncated compressed integer")
)

// AppendCompressedInt appends v to dst as a zchunk compressed integer and
// returns the extended slice. The encoding stores 7 value bits per byte, least
// significant group first. Per zchunk_format.txt the high bit (0x80) is CLEAR
// on every non-final byte and SET on the final byte — the inverse of LEB128's
// continuation convention.
func AppendCompressedInt(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)&0x7f) // non-final: high bit clear
		v >>= 7
	}
	return append(dst, byte(v)|0x80) // final: high bit set
}

// ReadCompressedInt decodes a single compressed integer from r. It returns
// ErrTruncated if the stream ends mid-integer and ErrOverflow if the encoded
// value would not fit in a uint64.
func ReadCompressedInt(r io.ByteReader) (uint64, error) {
	var v uint64
	for i := 0; i < MaxCompressedIntLen; i++ {
		b, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, ErrTruncated
			}
			return 0, err
		}
		// The 10th (final permissible) group only carries bit 63 of the value,
		// so its 7-bit payload must be 0 or 1.
		if i == MaxCompressedIntLen-1 && b&0x7f > 0x01 {
			return 0, ErrOverflow
		}
		v |= uint64(b&0x7f) << (7 * i)
		if b&0x80 != 0 { // high bit set => final byte
			return v, nil
		}
	}
	// All MaxCompressedIntLen groups were non-final: an 11th group would
	// follow, which cannot fit in a uint64.
	return 0, ErrOverflow
}

// ChecksumType identifies a digest algorithm used in a zchunk file. The header
// and total-data checksums use types 0–1; chunk checksums may also use 2–3.
type ChecksumType uint64

// Checksum types, per zchunk_format.txt.
const (
	SHA1      ChecksumType = 0 // 20-byte SHA-1
	SHA256    ChecksumType = 1 // 32-byte SHA-256
	SHA512    ChecksumType = 2 // 64-byte SHA-512
	SHA512128 ChecksumType = 3 // 16-byte SHA-512/128 (first 128 bits of SHA-512)
)

// Size returns the digest length in bytes for the checksum type, or an error
// for an unrecognised type.
func (t ChecksumType) Size() (int, error) {
	switch t {
	case SHA1:
		return 20, nil
	case SHA256:
		return 32, nil
	case SHA512:
		return 64, nil
	case SHA512128:
		return 16, nil
	default:
		return 0, fmt.Errorf("zchunk: unknown checksum type %d", uint64(t))
	}
}
