package zchunk

import (
	"bytes"
	"testing"
)

func TestCompressChunkRoundTrip(t *testing.T) {
	// Repetitive data so zstd actually shrinks it.
	src := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 64)
	for _, ct := range []CompressionType{CompressionNone, CompressionZstd} {
		comp, err := CompressChunk(ct, nil, src)
		if err != nil {
			t.Fatalf("ct=%d CompressChunk: %v", uint64(ct), err)
		}
		got, err := DecompressChunk(ct, nil, comp, uint64(len(src)))
		if err != nil {
			t.Fatalf("ct=%d DecompressChunk: %v", uint64(ct), err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("ct=%d round-trip mismatch", uint64(ct))
		}
	}
}

func TestCompressChunkEmptyAndNone(t *testing.T) {
	// Empty input round-trips for both codecs.
	for _, ct := range []CompressionType{CompressionNone, CompressionZstd} {
		comp, err := CompressChunk(ct, nil, nil)
		if err != nil {
			t.Fatalf("ct=%d CompressChunk(empty): %v", uint64(ct), err)
		}
		got, err := DecompressChunk(ct, nil, comp, 0)
		if err != nil {
			t.Fatalf("ct=%d DecompressChunk(empty): %v", uint64(ct), err)
		}
		if len(got) != 0 {
			t.Fatalf("ct=%d empty round-trip produced %d bytes", uint64(ct), len(got))
		}
	}
	// CompressionNone stores verbatim.
	src := []byte("verbatim")
	comp, err := CompressChunk(CompressionNone, nil, src)
	if err != nil {
		t.Fatalf("CompressChunk(none): %v", err)
	}
	if !bytes.Equal(comp, src) {
		t.Fatalf("CompressionNone altered the data: %q", comp)
	}
}

func TestCompressChunkWithDict(t *testing.T) {
	// The dict shares long runs with the payload, so dict-based compression is
	// both correct and smaller than dict-less compression.
	dict := bytes.Repeat([]byte("SHARED-PREFIX-CONTENT-"), 32)
	src := append(append([]byte(nil), dict...), []byte("-plus-a-unique-tail")...)

	withDict, err := CompressChunk(CompressionZstd, dict, src)
	if err != nil {
		t.Fatalf("CompressChunk(dict): %v", err)
	}
	got, err := DecompressChunk(CompressionZstd, dict, withDict, uint64(len(src)))
	if err != nil {
		t.Fatalf("DecompressChunk(dict): %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("dict round-trip mismatch")
	}

	noDict, err := CompressChunk(CompressionZstd, nil, src)
	if err != nil {
		t.Fatalf("CompressChunk(no dict): %v", err)
	}
	if len(withDict) >= len(noDict) {
		t.Fatalf("dict did not help: %d (dict) vs %d (no dict)", len(withDict), len(noDict))
	}
}

func TestCompressChunkUnsupportedType(t *testing.T) {
	if _, err := CompressChunk(CompressionType(1), nil, []byte("x")); err == nil {
		t.Fatal("CompressChunk accepted unsupported type")
	}
	if _, err := DecompressChunk(CompressionType(1), nil, []byte("x"), 1); err == nil {
		t.Fatal("DecompressChunk accepted unsupported type")
	}
}

func TestDecompressChunkErrors(t *testing.T) {
	// CompressionNone: stored length disagrees with the declared length.
	if _, err := DecompressChunk(CompressionNone, nil, []byte("abc"), 5); err == nil {
		t.Fatal("DecompressChunk(none) accepted a length mismatch")
	}
	// zstd: corrupt frame.
	if _, err := DecompressChunk(CompressionZstd, nil, []byte("not a zstd frame"), 1); err == nil {
		t.Fatal("DecompressChunk(zstd) accepted a corrupt frame")
	}
	// zstd: valid frame but declared length is wrong.
	comp, err := CompressChunk(CompressionZstd, nil, []byte("hello"))
	if err != nil {
		t.Fatalf("CompressChunk: %v", err)
	}
	if _, err := DecompressChunk(CompressionZstd, nil, comp, 99); err == nil {
		t.Fatal("DecompressChunk(zstd) accepted a wrong declared length")
	}
}
