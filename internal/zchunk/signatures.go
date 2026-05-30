package zchunk

import (
	"fmt"
	"io"
)

// Signatures is the zchunk header's signature section, which immediately
// follows the chunk index. Each signature applies only to the header (with the
// header size, header checksum and the signature section itself excluded), so
// signatures can be added without invalidating one another.
//
// The reference implementation does not yet recognise any signature type, so
// the only value valid on the wire is a zero count. A non-zero count is
// rejected on both read and write, matching the reference, which bails with
// "Signatures aren't supported yet".
type Signatures struct {
	// Count is the number of signatures; it must be zero until the format
	// defines a signature type.
	Count uint64
}

// ReadSignatures parses the signature section from r, leaving r positioned at
// the start of the body (the compressed dictionary and chunks).
func ReadSignatures(r io.Reader) (*Signatures, error) {
	count, err := ReadCompressedInt(byteReader{r})
	if err != nil {
		return nil, fmt.Errorf("zchunk: read signature count: %w", err)
	}
	if count > 0 {
		return nil, fmt.Errorf("zchunk: %d signature(s): signatures are not supported", count)
	}
	return &Signatures{Count: count}, nil
}

// WriteTo serialises the signature section to w. It rejects a non-zero count,
// since no signature type is supported yet.
func (s *Signatures) WriteTo(w io.Writer) (int64, error) {
	if s.Count > 0 {
		return 0, fmt.Errorf("zchunk: %d signature(s): signatures are not supported", s.Count)
	}
	n, err := w.Write(AppendCompressedInt(nil, s.Count))
	return int64(n), err
}
