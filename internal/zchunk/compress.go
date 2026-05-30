package zchunk

import (
	"fmt"

	"github.com/klauspost/compress/zstd"
)

// Each zchunk body chunk is a single self-contained unit: with CompressionNone
// it is stored verbatim, and with CompressionZstd it is one independent zstd
// frame. The dictionary is simply chunk 0's decompressed contents; chunks other
// than the dictionary are (de)compressed against it as a raw zstd dictionary,
// exactly as the reference does via ZSTD_compress_usingDict /
// ZSTD_decompress_usingDDict. The dictionary chunk itself is processed with an
// empty dict.

// CompressChunk compresses a single chunk's uncompressed bytes under ct. dict is
// the decompressed dictionary (chunk 0); pass nil/empty when compressing the
// dictionary chunk or any file without a dictionary. The result is the chunk's
// body bytes (one zstd frame, or src verbatim for CompressionNone). For
// compressing many chunks of one file, prefer a Builder, which reuses a single
// encoder rather than constructing one per call.
func CompressChunk(ct CompressionType, dict, src []byte) ([]byte, error) {
	if !ct.valid() {
		return nil, fmt.Errorf("zchunk: unsupported compression type %d", uint64(ct))
	}
	ce := newChunkEncoder(ct, dict)
	defer ce.close()
	return ce.compress(src), nil
}

// chunkEncoder compresses successive chunks of a single file, reusing one zstd
// encoder (bound to the file's dictionary) across all of them rather than
// constructing one per chunk — the write-path mirror of chunkDecoder. The zero
// encoder is unused for CompressionNone. A chunkEncoder is not safe for
// concurrent use; build one per file.
type chunkEncoder struct {
	ct  CompressionType
	enc *zstd.Encoder // nil for CompressionNone
}

// newChunkEncoder builds an encoder for ct bound to dict (the decompressed
// dictionary, or nil/empty for none). ct must already be valid; only none and
// zstd are handled.
func newChunkEncoder(ct CompressionType, dict []byte) *chunkEncoder {
	ce := &chunkEncoder{ct: ct}
	if ct == CompressionZstd {
		opts := []zstd.EOption{zstd.WithEncoderConcurrency(1)}
		if len(dict) > 0 {
			opts = append(opts, zstd.WithEncoderDictRaw(0, dict))
		}
		// NewWriter only fails on invalid options; ours are static and valid.
		ce.enc, _ = zstd.NewWriter(nil, opts...)
	}
	return ce
}

// compress encodes one chunk's bytes (verbatim for none, one zstd frame
// otherwise).
func (ce *chunkEncoder) compress(src []byte) []byte {
	if ce.ct == CompressionNone {
		return append([]byte(nil), src...)
	}
	return ce.enc.EncodeAll(src, nil)
}

// close releases the underlying zstd encoder, if any.
func (ce *chunkEncoder) close() {
	if ce.enc != nil {
		ce.enc.Close()
	}
}

// chunkDecoder decompresses successive chunks of a single file, reusing one
// zstd decoder (bound to the file's dictionary) across all of them rather than
// constructing one per chunk — mirroring the reference, which reuses a single
// ZSTD_DCtx. The zero decoder is unused for CompressionNone. A chunkDecoder is
// not safe for concurrent use; build one per Extract.
type chunkDecoder struct {
	ct  CompressionType
	dec *zstd.Decoder // nil for CompressionNone
}

// newChunkDecoder builds a decoder for ct bound to dict (the decompressed
// dictionary, or nil/empty for none). ct must already be valid (see
// CompressionType.valid); only none and zstd are handled.
func newChunkDecoder(ct CompressionType, dict []byte) *chunkDecoder {
	cd := &chunkDecoder{ct: ct}
	if ct == CompressionZstd {
		opts := []zstd.DOption{zstd.WithDecoderConcurrency(1)}
		if len(dict) > 0 {
			opts = append(opts, zstd.WithDecoderDictRaw(0, dict))
		}
		// NewReader only fails on invalid options; ours are static and valid.
		cd.dec, _ = zstd.NewReader(nil, opts...)
	}
	return cd
}

// decompress reverses one chunk's compression; the result must be exactly
// decompressedLen bytes.
func (cd *chunkDecoder) decompress(src []byte, decompressedLen uint64) ([]byte, error) {
	if cd.ct == CompressionNone {
		if uint64(len(src)) != decompressedLen {
			return nil, fmt.Errorf("zchunk: stored chunk length %d != declared %d", len(src), decompressedLen)
		}
		return append([]byte(nil), src...), nil
	}
	out, err := cd.dec.DecodeAll(src, nil)
	if err != nil {
		return nil, fmt.Errorf("zchunk: zstd decode: %w", err)
	}
	if uint64(len(out)) != decompressedLen {
		return nil, fmt.Errorf("zchunk: decompressed chunk length %d != declared %d", len(out), decompressedLen)
	}
	return out, nil
}

// close releases the underlying zstd decoder, if any.
func (cd *chunkDecoder) close() {
	if cd.dec != nil {
		cd.dec.Close()
	}
}

// DecompressChunk reverses CompressChunk for a single chunk. dict is the
// decompressed dictionary (chunk 0), or nil/empty for the dictionary chunk
// itself. decompressedLen is the index entry's Length; the result must match it
// exactly. For decompressing many chunks of one file, prefer reusing a decoder
// (see Extract) rather than calling this per chunk.
func DecompressChunk(ct CompressionType, dict, src []byte, decompressedLen uint64) ([]byte, error) {
	if !ct.valid() {
		return nil, fmt.Errorf("zchunk: unsupported compression type %d", uint64(ct))
	}
	cd := newChunkDecoder(ct, dict)
	defer cd.close()
	return cd.decompress(src, decompressedLen)
}
