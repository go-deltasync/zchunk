package zchunk

import (
	"fmt"
	"io"
)

// Preface flags (a bitmask stored as a compressed integer). A decoder MUST
// reject any file whose flags include a bit it does not recognise.
const (
	// FlagDataStreams indicates the file uses data streams (per-chunk stream IDs).
	FlagDataStreams uint64 = 1 << 0
	// FlagOptionalElements indicates the preface carries optional elements.
	FlagOptionalElements uint64 = 1 << 1
	// FlagUncompressedSource indicates the file may be applied against an
	// uncompressed source (chunks then carry an extra uncompressed checksum).
	FlagUncompressedSource uint64 = 1 << 2

	knownFlags = FlagDataStreams | FlagOptionalElements | FlagUncompressedSource
)

// CompressionType identifies how the dict and chunks are compressed.
type CompressionType uint64

// Compression types, per zchunk_format.txt (type 1 is unused).
const (
	CompressionNone CompressionType = 0
	CompressionZstd CompressionType = 2
)

func (c CompressionType) valid() bool {
	return c == CompressionNone || c == CompressionZstd
}

// OptionalElement is one entry of the preface's optional-element list. Its data
// is non-vital: a decoder that does not recognise ID must ignore the data.
type OptionalElement struct {
	ID   uint64
	Data []byte
}

// Preface is the zchunk metadata block that follows the lead:
//
//	data checksum | flags (ci) | compression type (ci) [ | optional elements ]
//
// The data checksum covers the whole body and uses the lead's checksum type, so
// ReadPreface must be told that type to know the checksum's length.
type Preface struct {
	// DataChecksum is the digest of the body; its length equals the lead's
	// ChecksumType.Size().
	DataChecksum []byte
	// Flags is the validated flag bitmask.
	Flags uint64
	// CompressionType is the dict/chunk compression algorithm.
	CompressionType CompressionType
	// OptionalElements is non-nil only when FlagOptionalElements is set.
	OptionalElements []OptionalElement
}

// HasDataStreams reports whether FlagDataStreams is set.
func (p *Preface) HasDataStreams() bool { return p.Flags&FlagDataStreams != 0 }

// HasOptionalElements reports whether FlagOptionalElements is set.
func (p *Preface) HasOptionalElements() bool { return p.Flags&FlagOptionalElements != 0 }

// UncompressedSource reports whether FlagUncompressedSource is set.
func (p *Preface) UncompressedSource() bool { return p.Flags&FlagUncompressedSource != 0 }

// ReadPreface parses a preface from r, where ckType is the checksum type taken
// from the lead. It leaves r positioned at the start of the index.
func ReadPreface(r io.Reader, ckType ChecksumType) (*Preface, error) {
	size, err := ckType.Size()
	if err != nil {
		return nil, err
	}
	sum := make([]byte, size)
	if _, err := io.ReadFull(r, sum); err != nil {
		return nil, fmt.Errorf("zchunk: read data checksum: %w", err)
	}

	br := byteReader{r}
	flags, err := ReadCompressedInt(br)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read flags: %w", err)
	}
	if flags&^knownFlags != 0 {
		return nil, fmt.Errorf("zchunk: unknown preface flags %#x", flags&^knownFlags)
	}

	rawComp, err := ReadCompressedInt(br)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read compression type: %w", err)
	}
	comp := CompressionType(rawComp)
	if !comp.valid() {
		return nil, fmt.Errorf("zchunk: unknown compression type %d", rawComp)
	}

	p := &Preface{DataChecksum: sum, Flags: flags, CompressionType: comp}
	if flags&FlagOptionalElements == 0 {
		return p, nil
	}

	count, err := ReadCompressedInt(br)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read optional element count: %w", err)
	}
	p.OptionalElements = make([]OptionalElement, 0, count)
	for i := uint64(0); i < count; i++ {
		id, err := ReadCompressedInt(br)
		if err != nil {
			return nil, fmt.Errorf("zchunk: read optional element %d id: %w", i, err)
		}
		dataSize, err := ReadCompressedInt(br)
		if err != nil {
			return nil, fmt.Errorf("zchunk: read optional element %d size: %w", i, err)
		}
		data := make([]byte, dataSize)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("zchunk: read optional element %d data: %w", i, err)
		}
		p.OptionalElements = append(p.OptionalElements, OptionalElement{ID: id, Data: data})
	}
	return p, nil
}

// WriteTo serialises the preface to w. It rejects unknown flags or compression
// types, and requires FlagOptionalElements to agree with whether any optional
// elements are present (as the format mandates).
func (p *Preface) WriteTo(w io.Writer) (int64, error) {
	if p.Flags&^knownFlags != 0 {
		return 0, fmt.Errorf("zchunk: unknown preface flags %#x", p.Flags&^knownFlags)
	}
	if !p.CompressionType.valid() {
		return 0, fmt.Errorf("zchunk: unknown compression type %d", uint64(p.CompressionType))
	}
	if (p.Flags&FlagOptionalElements != 0) != (len(p.OptionalElements) > 0) {
		return 0, fmt.Errorf("zchunk: FlagOptionalElements disagrees with %d optional elements",
			len(p.OptionalElements))
	}

	buf := make([]byte, 0, len(p.DataChecksum)+4*MaxCompressedIntLen)
	buf = append(buf, p.DataChecksum...)
	buf = AppendCompressedInt(buf, p.Flags)
	buf = AppendCompressedInt(buf, uint64(p.CompressionType))
	if len(p.OptionalElements) > 0 {
		buf = AppendCompressedInt(buf, uint64(len(p.OptionalElements)))
		for _, el := range p.OptionalElements {
			buf = AppendCompressedInt(buf, el.ID)
			buf = AppendCompressedInt(buf, uint64(len(el.Data)))
			buf = append(buf, el.Data...)
		}
	}

	n, err := w.Write(buf)
	return int64(n), err
}
