package zchunk

import (
	"bytes"
	"fmt"
	"io"
)

// Extract reconstructs a zchunk file's content from its body. r must be
// positioned at the start of the body — immediately after the header, i.e. after
// ReadSignatures — and ct is the preface's compression type. Extract reads the
// dictionary (chunk 0) and the data chunks in order, verifies each non-empty
// chunk against its index digest, decompresses it (data chunks against the
// dictionary), and writes the reconstructed content to out. The dictionary chunk
// is used only as the zstd dictionary and is not itself written. It returns the
// number of bytes written.
func (idx *Index) Extract(r io.Reader, ct CompressionType, out io.Writer) (int64, error) {
	// Validate the chunk checksum type once so per-chunk hashing cannot fail.
	if _, err := idx.ChunkChecksumType.Size(); err != nil {
		return 0, err
	}
	if !ct.valid() {
		return 0, fmt.Errorf("zchunk: unsupported compression type %d", uint64(ct))
	}
	if len(idx.Chunks) == 0 {
		return 0, nil
	}

	// Chunk 0 is the dictionary; decompress it (with no dictionary of its own)
	// for use as the zstd dictionary of the remaining chunks. An empty dict
	// (zero lengths, conventionally an all-zero digest) yields nil.
	dictDec := newChunkDecoder(ct, nil)
	dict, err := idx.readChunk(r, dictDec, idx.Chunks[0], 0)
	dictDec.close()
	if err != nil {
		return 0, err
	}

	// Reuse a single decoder bound to the dictionary for every data chunk,
	// instead of constructing one per chunk (as the reference reuses one DCtx).
	dec := newChunkDecoder(ct, dict)
	defer dec.close()
	var written int64
	for i := 1; i < len(idx.Chunks); i++ {
		data, err := idx.readChunk(r, dec, idx.Chunks[i], i)
		if err != nil {
			return written, err
		}
		n, err := out.Write(data)
		written += int64(n)
		if err != nil {
			return written, fmt.Errorf("zchunk: write chunk %d: %w", i, err)
		}
	}
	return written, nil
}

// readChunk reads one chunk's compressed body (CompLength bytes) from r,
// verifies it against e.Digest, decompresses it (against dict) and verifies the
// result against e.UncompressedDigest when present. An empty chunk (CompLength
// 0) is treated as empty content with no verification, since the reference
// leaves an empty dictionary's digest all-zero. n is the chunk number, for
// error messages. The chunk checksum type must already be validated.
func (idx *Index) readChunk(r io.Reader, dec *chunkDecoder, e IndexEntry, n int) ([]byte, error) {
	comp := make([]byte, e.CompLength)
	if _, err := io.ReadFull(r, comp); err != nil {
		return nil, fmt.Errorf("zchunk: read chunk %d body: %w", n, err)
	}
	if e.CompLength == 0 {
		return nil, nil
	}
	// Digest covers the compressed bytes (so peers can match chunks before
	// decompressing). Type is pre-validated, so Sum cannot error here.
	sum, _ := idx.ChunkChecksumType.Sum(comp)
	if !bytes.Equal(sum, e.Digest) {
		return nil, fmt.Errorf("zchunk: chunk %d digest mismatch", n)
	}
	data, err := dec.decompress(comp, e.Length)
	if err != nil {
		return nil, fmt.Errorf("zchunk: chunk %d: %w", n, err)
	}
	if e.UncompressedDigest != nil {
		usum, _ := idx.ChunkChecksumType.Sum(data)
		if !bytes.Equal(usum, e.UncompressedDigest) {
			return nil, fmt.Errorf("zchunk: chunk %d uncompressed digest mismatch", n)
		}
	}
	return data, nil
}
