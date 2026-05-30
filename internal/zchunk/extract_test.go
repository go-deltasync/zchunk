package zchunk

import (
	"bytes"
	"io"
	"testing"
)

// buildBody assembles an index and body bytes for a dictionary plus data chunks,
// mirroring the reference: chunk 0 is the dictionary (compressed without a dict),
// and data chunks are compressed against the dictionary's plaintext. An empty
// chunk is stored as zero bytes with an all-zero digest, as the reference does.
func buildBody(t *testing.T, ck ChecksumType, ct CompressionType, uncompressed bool,
	dictPlain []byte, dataPlain [][]byte) (*Index, []byte) {
	t.Helper()
	size, err := ck.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	idx := &Index{ChunkChecksumType: ck}
	var body bytes.Buffer

	add := func(plain, dict []byte) {
		var comp []byte
		if len(plain) > 0 {
			comp, err = CompressChunk(ct, dict, plain)
			if err != nil {
				t.Fatalf("CompressChunk: %v", err)
			}
		}
		e := IndexEntry{CompLength: uint64(len(comp)), Length: uint64(len(plain))}
		if len(comp) > 0 {
			if e.Digest, err = ck.Sum(comp); err != nil {
				t.Fatalf("Sum: %v", err)
			}
		} else {
			e.Digest = make([]byte, size) // empty chunk: all-zero digest
		}
		if uncompressed {
			if len(comp) > 0 {
				if e.UncompressedDigest, err = ck.Sum(plain); err != nil {
					t.Fatalf("Sum: %v", err)
				}
			} else {
				e.UncompressedDigest = make([]byte, size)
			}
		}
		idx.Chunks = append(idx.Chunks, e)
		body.Write(comp)
	}

	add(dictPlain, nil)
	for _, dp := range dataPlain {
		add(dp, dictPlain)
	}
	return idx, body.Bytes()
}

func TestExtractRoundTrip(t *testing.T) {
	dict := bytes.Repeat([]byte("SHARED-DICTIONARY-CONTENT-"), 16)
	data := [][]byte{
		append(append([]byte(nil), dict...), []byte(" first chunk tail")...),
		[]byte("second chunk, quite different content here"),
		{}, // an empty data chunk
	}
	cases := []struct {
		name         string
		ck           ChecksumType
		ct           CompressionType
		uncompressed bool
		dict         []byte
	}{
		{"zstd-dict", SHA256, CompressionZstd, false, dict},
		{"zstd-empty-dict", SHA256, CompressionZstd, false, nil},
		{"none-empty-dict", SHA1, CompressionNone, false, nil},
		{"zstd-uncompressed", SHA512, CompressionZstd, true, dict},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, body := buildBody(t, c.ck, c.ct, c.uncompressed, c.dict, data)
			var out bytes.Buffer
			n, err := idx.Extract(bytes.NewReader(body), c.ct, &out)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			var want bytes.Buffer
			for _, d := range data {
				want.Write(d)
			}
			if int(n) != want.Len() {
				t.Fatalf("Extract n=%d, want %d", n, want.Len())
			}
			if !bytes.Equal(out.Bytes(), want.Bytes()) {
				t.Fatal("extracted content mismatch")
			}
		})
	}
}

func TestExtractEmptyIndex(t *testing.T) {
	idx := &Index{ChunkChecksumType: SHA256}
	var out bytes.Buffer
	n, err := idx.Extract(bytes.NewReader(nil), CompressionZstd, &out)
	if err != nil || n != 0 || out.Len() != 0 {
		t.Fatalf("Extract(empty) = (%d, %v), out=%d; want (0, nil, 0)", n, err, out.Len())
	}
}

func TestExtractBadChecksumType(t *testing.T) {
	idx := &Index{ChunkChecksumType: 99}
	if _, err := idx.Extract(bytes.NewReader(nil), CompressionZstd, io.Discard); err == nil {
		t.Fatal("Extract accepted an unknown chunk checksum type")
	}
}

func TestExtractBadCompressionType(t *testing.T) {
	idx := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: make([]byte, 32), CompLength: 0, Length: 0},
	}}
	if _, err := idx.Extract(bytes.NewReader(nil), CompressionType(99), io.Discard); err == nil {
		t.Fatal("Extract accepted an unknown compression type")
	}
}

func TestExtractErrors(t *testing.T) {
	digOf := func(b []byte) []byte { d, _ := SHA256.Sum(b); return d }
	zero := make([]byte, 32)
	emptyDict := IndexEntry{Digest: zero, CompLength: 0, Length: 0}

	t.Run("dict-read-error", func(t *testing.T) {
		idx := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
			{Digest: digOf([]byte("x")), CompLength: 5, Length: 5},
		}}
		// Body shorter than the declared dict chunk.
		if _, err := idx.Extract(bytes.NewReader([]byte{1, 2}), CompressionZstd, io.Discard); err == nil {
			t.Fatal("expected dict read error")
		}
	})

	t.Run("data-read-error", func(t *testing.T) {
		idx := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
			emptyDict,
			{Digest: digOf([]byte("garbage")), CompLength: 7, Length: 5},
		}}
		if _, err := idx.Extract(bytes.NewReader([]byte("abc")), CompressionZstd, io.Discard); err == nil {
			t.Fatal("expected data read error")
		}
	})

	t.Run("digest-mismatch", func(t *testing.T) {
		body := []byte("garbage")
		idx := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
			emptyDict,
			{Digest: bytes.Repeat([]byte{0xff}, 32), CompLength: uint64(len(body)), Length: 5},
		}}
		if _, err := idx.Extract(bytes.NewReader(body), CompressionNone, io.Discard); err == nil {
			t.Fatal("expected digest mismatch")
		}
	})

	t.Run("decompress-error", func(t *testing.T) {
		body := []byte("not a zstd frame")
		idx := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
			emptyDict,
			{Digest: digOf(body), CompLength: uint64(len(body)), Length: 5},
		}}
		if _, err := idx.Extract(bytes.NewReader(body), CompressionZstd, io.Discard); err == nil {
			t.Fatal("expected decompress error")
		}
	})

	t.Run("uncompressed-digest-mismatch", func(t *testing.T) {
		idx, body := buildBody(t, SHA256, CompressionZstd, true, nil, [][]byte{[]byte("payload")})
		idx.Chunks[1].UncompressedDigest = bytes.Repeat([]byte{0xff}, 32)
		if _, err := idx.Extract(bytes.NewReader(body), CompressionZstd, io.Discard); err == nil {
			t.Fatal("expected uncompressed digest mismatch")
		}
	})

	t.Run("write-error", func(t *testing.T) {
		idx, body := buildBody(t, SHA256, CompressionZstd, false, nil, [][]byte{[]byte("payload")})
		if _, err := idx.Extract(bytes.NewReader(body), CompressionZstd, &failWriter{failAt: 1}); err == nil {
			t.Fatal("expected write error")
		}
	})
}
