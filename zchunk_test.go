package zchunk_test

import (
	"bytes"
	"testing"

	"github.com/go-deltasync/zchunk"
)

// TestFacadeChunkRoundTrip exercises the public API (compress + decompress a
// chunk) to confirm the façade is wired to the internal package.
func TestFacadeChunkRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("go-deltasync zchunk facade payload "), 200)
	comp, err := zchunk.CompressChunk(zchunk.CompressionZstd, nil, data)
	if err != nil {
		t.Fatalf("CompressChunk: %v", err)
	}
	got, err := zchunk.DecompressChunk(zchunk.CompressionZstd, nil, comp, uint64(len(data)))
	if err != nil {
		t.Fatalf("DecompressChunk: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("façade chunk round-trip mismatch")
	}
}
