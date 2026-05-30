package zchunk

import (
	"fmt"
	"io"
)

// Builder assembles a zchunk file's index and body chunk by chunk while reusing
// a single zstd encoder bound to the file's dictionary, instead of constructing
// a fresh encoder per chunk as CompressChunk does. This mirrors the read path's
// single-decoder reuse (see chunkDecoder) and the reference's single ZSTD_CCtx,
// avoiding the ~1.8 MB/op a per-chunk encoder allocates.
//
// Typical use: NewBuilder, then AddChunk for each data chunk, then WriteFile (or
// read Index/Body to drive WriteFile/WriteDetachedHeader directly). A Builder is
// not safe for concurrent use, and Close must be called to release the encoder.
type Builder struct {
	enc  *chunkEncoder
	idx  *Index
	body []byte
}

// NewBuilder creates a Builder for compression type ct, recording chunk digests
// with chunkChecksum. dict is the file's dictionary (decompressed); pass
// nil/empty for a file without one. Chunk 0 is the dictionary: an empty
// dictionary is recorded as a zero-length chunk with an all-zero digest
// (matching the reference and unzck), while a real dictionary is compressed
// against an empty dict and hashed like any chunk. It fails for an unknown
// compression type or chunk checksum type.
func NewBuilder(ct CompressionType, chunkChecksum ChecksumType, dict []byte) (*Builder, error) {
	if !ct.valid() {
		return nil, fmt.Errorf("zchunk: unsupported compression type %d", uint64(ct))
	}
	digestSize, err := chunkChecksum.Size()
	if err != nil {
		return nil, err
	}
	b := &Builder{
		enc: newChunkEncoder(ct, dict),
		idx: &Index{ChunkChecksumType: chunkChecksum},
	}
	if len(dict) == 0 {
		b.idx.Chunks = append(b.idx.Chunks, IndexEntry{Digest: make([]byte, digestSize)})
		return b, nil
	}
	// A real dictionary chunk is coded against an empty dict (the encoder bound
	// to dict is for the data chunks); ct and chunkChecksum are validated above,
	// so neither call can fail here.
	comp, _ := CompressChunk(ct, nil, dict)
	digest, _ := chunkChecksum.Sum(comp)
	b.idx.Chunks = append(b.idx.Chunks, IndexEntry{
		Digest:     digest,
		CompLength: uint64(len(comp)),
		Length:     uint64(len(dict)),
	})
	b.body = append(b.body, comp...)
	return b, nil
}

// AddChunk compresses one chunk of plaintext (against the dictionary), appends
// its compressed bytes to the body and records its index entry. The chunk
// checksum type was validated in NewBuilder, so this cannot fail.
func (b *Builder) AddChunk(plain []byte) {
	comp := b.enc.compress(plain)
	digest, _ := b.idx.ChunkChecksumType.Sum(comp)
	b.idx.Chunks = append(b.idx.Chunks, IndexEntry{
		Digest:     digest,
		CompLength: uint64(len(comp)),
		Length:     uint64(len(plain)),
	})
	b.body = append(b.body, comp...)
}

// Index returns the chunk index assembled so far (dictionary at chunk 0). The
// returned value is owned by the Builder; do not mutate it.
func (b *Builder) Index() *Index { return b.idx }

// Body returns the assembled body bytes: the compressed dictionary chunk
// followed by every compressed data chunk. The slice is owned by the Builder.
func (b *Builder) Body() []byte { return b.body }

// WriteFile finalises the build by writing a complete zchunk file to w with the
// given overall checksum type, preface and signatures (see the package-level
// WriteFile for the layout and the checksums it derives).
func (b *Builder) WriteFile(w io.Writer, overallType ChecksumType, pre *Preface, sigs *Signatures) (int64, error) {
	return WriteFile(w, overallType, pre, b.idx, sigs, b.body)
}

// Close releases the Builder's zstd encoder. The Builder must not be used after
// Close.
func (b *Builder) Close() { b.enc.close() }
