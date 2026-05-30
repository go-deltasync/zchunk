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
// body bytes (one zstd frame, or src verbatim for CompressionNone).
func CompressChunk(ct CompressionType, dict, src []byte) ([]byte, error) {
	switch ct {
	case CompressionNone:
		return append([]byte(nil), src...), nil
	case CompressionZstd:
		opts := []zstd.EOption{zstd.WithEncoderConcurrency(1)}
		if len(dict) > 0 {
			opts = append(opts, zstd.WithEncoderDictRaw(0, dict))
		}
		// NewWriter only fails on invalid options; ours are static and valid.
		enc, _ := zstd.NewWriter(nil, opts...)
		defer enc.Close()
		return enc.EncodeAll(src, nil), nil
	default:
		return nil, fmt.Errorf("zchunk: unsupported compression type %d", uint64(ct))
	}
}

// DecompressChunk reverses CompressChunk. dict is the decompressed dictionary
// (chunk 0), or nil/empty for the dictionary chunk itself. decompressedLen is
// the index entry's Length; the result must match it exactly.
func DecompressChunk(ct CompressionType, dict, src []byte, decompressedLen uint64) ([]byte, error) {
	switch ct {
	case CompressionNone:
		if uint64(len(src)) != decompressedLen {
			return nil, fmt.Errorf("zchunk: stored chunk length %d != declared %d", len(src), decompressedLen)
		}
		return append([]byte(nil), src...), nil
	case CompressionZstd:
		opts := []zstd.DOption{zstd.WithDecoderConcurrency(1)}
		if len(dict) > 0 {
			opts = append(opts, zstd.WithDecoderDictRaw(0, dict))
		}
		// NewReader only fails on invalid options; ours are static and valid.
		dec, _ := zstd.NewReader(nil, opts...)
		defer dec.Close()
		out, err := dec.DecodeAll(src, nil)
		if err != nil {
			return nil, fmt.Errorf("zchunk: zstd decode: %w", err)
		}
		if uint64(len(out)) != decompressedLen {
			return nil, fmt.Errorf("zchunk: decompressed chunk length %d != declared %d", len(out), decompressedLen)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("zchunk: unsupported compression type %d", uint64(ct))
	}
}
