// Package zchunk implements primitives of the zchunk file format — a
// content-defined-chunked container (as used by Fedora's DNF/librepo) that
// supports delta downloads: a client fetches only the chunks it is missing
// over HTTP range requests, recovering the rest from a local copy.
//
// This package currently provides the format's foundational building blocks:
// the file magic and the variable-length "compressed integer" codec that the
// lead, preface and chunk index all rely on. The full lead/preface/index and
// zstd handling is tracked in a dedicated design plan and will land here
// incrementally — every addition under the org's 100%-coverage rule.
package zchunk

import (
	"errors"
	"io"
)

// Magic is the 5-byte lead that prefixes every zchunk file: a NUL byte
// followed by the ASCII bytes "ZCK1".
const Magic = "\x00ZCK1"

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
// significant group first, with the high bit (0x80) set on every byte except
// the final one. This is the unsigned LEB128 encoding zchunk uses throughout
// its lead, preface and index.
func AppendCompressedInt(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
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
		if b&0x80 == 0 {
			return v, nil
		}
	}
	// All MaxCompressedIntLen groups had the continuation bit set: an 11th
	// group would follow, which cannot fit in a uint64.
	return 0, ErrOverflow
}
