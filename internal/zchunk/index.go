package zchunk

import (
	"bytes"
	"fmt"
	"io"
)

// IndexEntry describes one chunk in the index. Chunk 0 of an index is the
// dictionary (which may be empty: zero lengths and an all-zero digest).
type IndexEntry struct {
	// Digest is the checksum of the compressed chunk; its length equals the
	// index's ChunkChecksumType.Size().
	Digest []byte
	// UncompressedDigest is the checksum of the uncompressed chunk. It is
	// present (non-nil, same length as Digest) only when the file's preface
	// sets FlagUncompressedSource.
	UncompressedDigest []byte
	// CompLength is the chunk's length in the body (compressed).
	CompLength uint64
	// Length is the chunk's length after decompression.
	Length uint64
}

// Index is the chunk index of a zchunk file. Per the reference implementation
// the dictionary is simply Chunks[0], read with the same layout as any chunk,
// and there is no per-chunk "stream" field.
type Index struct {
	// ChunkChecksumType is the digest algorithm for chunk checksums; it may
	// differ from the lead's overall checksum type.
	ChunkChecksumType ChecksumType
	// Chunks lists every entry; Chunks[0], when present, is the dictionary.
	Chunks []IndexEntry
}

// Dict returns the dictionary entry (chunk 0), or false if the index is empty.
func (idx *Index) Dict() (IndexEntry, bool) {
	if len(idx.Chunks) == 0 {
		return IndexEntry{}, false
	}
	return idx.Chunks[0], true
}

// ReadIndex parses an index from r. uncompressedSource must reflect the
// preface's FlagUncompressedSource, since it governs whether each entry carries
// an extra uncompressed digest. It leaves r positioned at the signatures.
func ReadIndex(r io.Reader, uncompressedSource bool) (*Index, error) {
	indexSize, err := ReadCompressedInt(byteReader{r})
	if err != nil {
		return nil, fmt.Errorf("zchunk: read index size: %w", err)
	}
	buf := make([]byte, indexSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("zchunk: read index body: %w", err)
	}
	return parseIndex(buf, uncompressedSource)
}

func parseIndex(buf []byte, uncompressedSource bool) (*Index, error) {
	rd := bytes.NewReader(buf)
	rawType, err := ReadCompressedInt(rd)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read chunk checksum type: %w", err)
	}
	ckType := ChecksumType(rawType)
	digestSize, err := ckType.Size()
	if err != nil {
		return nil, err
	}
	declaredCount, err := ReadCompressedInt(rd)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read chunk count: %w", err)
	}

	idx := &Index{ChunkChecksumType: ckType}
	// The index body is consumed entry-by-entry until exhausted; chunk count is
	// a corroborating value, validated at the end.
	for rd.Len() > 0 {
		n := len(idx.Chunks)
		var e IndexEntry
		e.Digest = make([]byte, digestSize)
		if _, err := io.ReadFull(rd, e.Digest); err != nil {
			return nil, fmt.Errorf("zchunk: read chunk %d digest: %w", n, err)
		}
		if uncompressedSource {
			e.UncompressedDigest = make([]byte, digestSize)
			if _, err := io.ReadFull(rd, e.UncompressedDigest); err != nil {
				return nil, fmt.Errorf("zchunk: read chunk %d uncompressed digest: %w", n, err)
			}
		}
		if e.CompLength, err = ReadCompressedInt(rd); err != nil {
			return nil, fmt.Errorf("zchunk: read chunk %d length: %w", n, err)
		}
		if e.Length, err = ReadCompressedInt(rd); err != nil {
			return nil, fmt.Errorf("zchunk: read chunk %d uncompressed length: %w", n, err)
		}
		idx.Chunks = append(idx.Chunks, e)
	}
	if uint64(len(idx.Chunks)) != declaredCount {
		return nil, fmt.Errorf("zchunk: chunk count %d != %d entries read", declaredCount, len(idx.Chunks))
	}
	return idx, nil
}

// WriteTo serialises the index to w. uncompressedSource must match the value
// used when the file's preface was written. It rejects an unknown chunk
// checksum type or any entry whose digest length disagrees with that type.
func (idx *Index) WriteTo(w io.Writer, uncompressedSource bool) (int64, error) {
	digestSize, err := idx.ChunkChecksumType.Size()
	if err != nil {
		return 0, err
	}

	var body []byte
	body = AppendCompressedInt(body, uint64(idx.ChunkChecksumType))
	body = AppendCompressedInt(body, uint64(len(idx.Chunks)))
	for i, e := range idx.Chunks {
		if len(e.Digest) != digestSize {
			return 0, fmt.Errorf("zchunk: chunk %d digest length %d, want %d", i, len(e.Digest), digestSize)
		}
		body = append(body, e.Digest...)
		if uncompressedSource {
			if len(e.UncompressedDigest) != digestSize {
				return 0, fmt.Errorf("zchunk: chunk %d uncompressed digest length %d, want %d",
					i, len(e.UncompressedDigest), digestSize)
			}
			body = append(body, e.UncompressedDigest...)
		}
		body = AppendCompressedInt(body, e.CompLength)
		body = AppendCompressedInt(body, e.Length)
	}

	out := AppendCompressedInt(nil, uint64(len(body)))
	out = append(out, body...)
	n, err := w.Write(out)
	return int64(n), err
}
