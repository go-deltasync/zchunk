package zchunk

import (
	"fmt"
	"io"
)

// Lead is the fixed prologue of a zchunk file: everything needed to locate and
// validate the header. It is laid out as
//
//	ID (5 bytes) | checksum type (ci) | header size (ci) | header checksum
//
// where the header checksum is a digest whose length is fixed by the checksum
// type, and the header size counts the preface+index+signatures that follow
// the lead.
type Lead struct {
	// Detached reports whether the ID was DetachedMagic ("\0ZHR1") rather than
	// Magic ("\0ZCK1").
	Detached bool
	// ChecksumType is the algorithm used for the header and total-data checksums.
	ChecksumType ChecksumType
	// HeaderSize is the byte length of the header (preface+index+signatures),
	// not including the lead.
	HeaderSize uint64
	// HeaderChecksum is the digest of the header; its length equals
	// ChecksumType.Size().
	HeaderChecksum []byte
}

// byteReader adapts an io.Reader to io.ByteReader without buffering, so reads
// stop exactly at the end of each value and the underlying reader stays
// positioned for the next section.
type byteReader struct{ r io.Reader }

func (b byteReader) ReadByte() (byte, error) {
	var p [1]byte
	if _, err := io.ReadFull(b.r, p[:]); err != nil {
		return 0, err
	}
	return p[0], nil
}

// ReadLead parses a lead from r, consuming exactly its bytes and leaving r
// positioned at the start of the header.
func ReadLead(r io.Reader) (*Lead, error) {
	id := make([]byte, len(Magic))
	if _, err := io.ReadFull(r, id); err != nil {
		return nil, fmt.Errorf("zchunk: read lead id: %w", err)
	}
	var detached bool
	switch string(id) {
	case Magic:
		detached = false
	case DetachedMagic:
		detached = true
	default:
		return nil, fmt.Errorf("zchunk: bad lead magic %#x", id)
	}

	br := byteReader{r}
	rawType, err := ReadCompressedInt(br)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read checksum type: %w", err)
	}
	ckType := ChecksumType(rawType)
	size, err := ckType.Size()
	if err != nil {
		return nil, err
	}

	headerSize, err := ReadCompressedInt(br)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read header size: %w", err)
	}

	sum := make([]byte, size)
	if _, err := io.ReadFull(r, sum); err != nil {
		return nil, fmt.Errorf("zchunk: read header checksum: %w", err)
	}

	return &Lead{
		Detached:       detached,
		ChecksumType:   ckType,
		HeaderSize:     headerSize,
		HeaderChecksum: sum,
	}, nil
}

// WriteTo serialises the lead to w. It reports an error if the checksum type is
// unknown or if HeaderChecksum's length disagrees with that type's digest size.
func (l *Lead) WriteTo(w io.Writer) (int64, error) {
	size, err := l.ChecksumType.Size()
	if err != nil {
		return 0, err
	}
	if len(l.HeaderChecksum) != size {
		return 0, fmt.Errorf("zchunk: header checksum length %d, want %d for checksum type %d",
			len(l.HeaderChecksum), size, uint64(l.ChecksumType))
	}

	magic := Magic
	if l.Detached {
		magic = DetachedMagic
	}
	buf := make([]byte, 0, len(magic)+2*MaxCompressedIntLen+size)
	buf = append(buf, magic...)
	buf = AppendCompressedInt(buf, uint64(l.ChecksumType))
	buf = AppendCompressedInt(buf, l.HeaderSize)
	buf = append(buf, l.HeaderChecksum...)

	n, err := w.Write(buf)
	return int64(n), err
}
