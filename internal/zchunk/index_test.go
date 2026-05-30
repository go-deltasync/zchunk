package zchunk

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

// idxCI is a local shorthand for encoding a compressed integer.
func idxCI(v uint64) []byte { return AppendCompressedInt(nil, v) }

// concatBytes joins byte slices.
func concatBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestIndexRoundTrip(t *testing.T) {
	cases := []struct {
		name         string
		uncompressed bool
		idx          *Index
	}{
		{
			name:         "sha256-no-uncompressed",
			uncompressed: false,
			idx: &Index{
				ChunkChecksumType: SHA256,
				Chunks: []IndexEntry{
					// Chunk 0 is the (empty) dictionary.
					{Digest: cksum(t, SHA256, 0x00), CompLength: 0, Length: 0},
					{Digest: cksum(t, SHA256, 0x11), CompLength: 100, Length: 250},
					{Digest: cksum(t, SHA256, 0x22), CompLength: 50, Length: 80},
				},
			},
		},
		{
			name:         "sha1-with-uncompressed",
			uncompressed: true,
			idx: &Index{
				ChunkChecksumType: SHA1,
				Chunks: []IndexEntry{
					{
						Digest:             cksum(t, SHA1, 0xaa),
						UncompressedDigest: cksum(t, SHA1, 0xbb),
						CompLength:         10,
						Length:             20,
					},
					{
						Digest:             cksum(t, SHA1, 0xcc),
						UncompressedDigest: cksum(t, SHA1, 0xdd),
						CompLength:         4096,
						Length:             8192,
					},
				},
			},
		},
		{
			name:         "empty",
			uncompressed: false,
			idx:          &Index{ChunkChecksumType: SHA512, Chunks: nil},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			n, err := c.idx.WriteTo(&buf, c.uncompressed)
			if err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
			if int(n) != buf.Len() {
				t.Fatalf("WriteTo n=%d, buffer=%d", n, buf.Len())
			}
			got, err := ReadIndex(bytes.NewReader(buf.Bytes()), c.uncompressed)
			if err != nil {
				t.Fatalf("ReadIndex: %v", err)
			}
			if !reflect.DeepEqual(got, c.idx) {
				t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, c.idx)
			}
		})
	}
}

func TestIndexDict(t *testing.T) {
	empty := &Index{ChunkChecksumType: SHA256}
	if _, ok := empty.Dict(); ok {
		t.Fatal("empty index reported a dictionary")
	}
	withDict := &Index{
		ChunkChecksumType: SHA256,
		Chunks:            []IndexEntry{{Digest: cksum(t, SHA256, 0x00), CompLength: 7, Length: 9}},
	}
	d, ok := withDict.Dict()
	if !ok {
		t.Fatal("index with chunks reported no dictionary")
	}
	if d.CompLength != 7 || d.Length != 9 {
		t.Fatalf("Dict() = %+v, want CompLength=7 Length=9", d)
	}
}

func TestReadIndexErrors(t *testing.T) {
	cases := []struct {
		name         string
		data         []byte
		uncompressed bool
	}{
		{"index-size-read-error", nil, false},
		// Declares a 10-byte body but supplies only 3.
		{"short-index-body", concatBytes(idxCI(10), []byte{1, 2, 3}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ReadIndex(bytes.NewReader(c.data), c.uncompressed); err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestParseIndexErrors(t *testing.T) {
	d256 := cksum(t, SHA256, 0x33)
	d1 := cksum(t, SHA1, 0x44)

	cases := []struct {
		name         string
		body         []byte
		uncompressed bool
	}{
		{"chunk-checksum-type-read-error", nil, false},
		{"unknown-checksum-type", idxCI(99), false},
		{"chunk-count-read-error", idxCI(uint64(SHA256)), false},
		// One declared chunk, digest truncated.
		{"digest-read-error", concatBytes(idxCI(uint64(SHA256)), idxCI(1), d256[:10]), false},
		// uncompressed source: full digest then truncated uncompressed digest.
		{"uncompressed-digest-read-error", concatBytes(idxCI(uint64(SHA1)), idxCI(1), d1, d1[:5]), true},
		// Digest read, but comp length missing.
		{"comp-length-read-error", concatBytes(idxCI(uint64(SHA256)), idxCI(1), d256), false},
		// Digest + comp length, but length missing.
		{"length-read-error", concatBytes(idxCI(uint64(SHA256)), idxCI(1), d256, idxCI(100)), false},
		// Declares 2 entries but only one is present.
		{"count-mismatch", concatBytes(idxCI(uint64(SHA256)), idxCI(2), d256, idxCI(1), idxCI(2)), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseIndex(c.body, c.uncompressed); err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestIndexWriteToErrors(t *testing.T) {
	// Unknown chunk checksum type.
	unknown := &Index{ChunkChecksumType: 99}
	if _, err := unknown.WriteTo(io.Discard, false); err == nil {
		t.Fatal("WriteTo accepted unknown checksum type")
	}

	// Digest length disagrees with the checksum type.
	badDigest := &Index{
		ChunkChecksumType: SHA256,
		Chunks:            []IndexEntry{{Digest: make([]byte, 4), CompLength: 1, Length: 1}},
	}
	if _, err := badDigest.WriteTo(io.Discard, false); err == nil {
		t.Fatal("WriteTo accepted mismatched digest length")
	}

	// Uncompressed digest length disagrees with the checksum type.
	badUncompressed := &Index{
		ChunkChecksumType: SHA256,
		Chunks: []IndexEntry{{
			Digest:             cksum(t, SHA256, 0x11),
			UncompressedDigest: make([]byte, 4),
			CompLength:         1,
			Length:             1,
		}},
	}
	if _, err := badUncompressed.WriteTo(io.Discard, true); err == nil {
		t.Fatal("WriteTo accepted mismatched uncompressed digest length")
	}

	// Underlying writer fails.
	good := &Index{
		ChunkChecksumType: SHA256,
		Chunks:            []IndexEntry{{Digest: cksum(t, SHA256, 0x11), CompLength: 1, Length: 1}},
	}
	if _, err := good.WriteTo(&failWriter{failAt: 1}, false); !errors.Is(err, errBoom) {
		t.Fatalf("WriteTo write error = %v, want errBoom", err)
	}
}
