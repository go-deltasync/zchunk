//go:build compat
// +build compat

// Package zchunk compat-tag tests verify that our parser, writer and chunk
// codec inter-operate with the C reference implementation
// (https://github.com/zchunk/zchunk, the `zck`/`unzck` tools) byte-for-byte.
//
// The tests are gated by the `compat` build tag so a plain `go test ./...`
// does not depend on external binaries; CI runs them via the
// .github/workflows/compat.yml workflow, which installs the `zchunk` package
// before invoking `go test -tags=compat ./internal/zchunk/...`.
//
// Each test skips cleanly if the C tool it needs is missing from PATH, so a
// developer who hasn't installed the reference sees a skip, not a failure.
package zchunk

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// lookTool skips the test/benchmark unless name is on PATH, returning its full
// path.
func lookTool(tb testing.TB, name string) string {
	tb.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		tb.Skipf("%s not on PATH (%v) — install via `apt-get install zchunk` / `brew install zchunk`", name, err)
	}
	return p
}

// randBytes returns n bytes from crypto/rand.
func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// extractZck parses the full header from a reader over a .zck file and returns
// the reconstructed content.
func extractZck(t *testing.T, r io.Reader) []byte {
	t.Helper()
	lead, err := ReadLead(r)
	if err != nil {
		t.Fatalf("ReadLead: %v", err)
	}
	pre, err := ReadPreface(r, lead.ChecksumType)
	if err != nil {
		t.Fatalf("ReadPreface: %v", err)
	}
	idx, err := ReadIndex(r, pre.UncompressedSource())
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if _, err := ReadSignatures(r); err != nil {
		t.Fatalf("ReadSignatures: %v", err)
	}
	var out bytes.Buffer
	if _, err := idx.Extract(r, pre.CompressionType, &out); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return out.Bytes()
}

// TestCompatZckToOurExtract compresses a file with the C `zck` tool (default
// zstd + SHA-256), then verifies our reader + Extract reproduce the original
// bytes — exercising the full read path against real reference output.
func TestCompatZckToOurExtract(t *testing.T) {
	zck := lookTool(t, "zck")

	dir := t.TempDir()
	orig := randBytes(t, 300*1024) // 300 KB, several chunks
	src := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(src, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	zckPath := filepath.Join(dir, "data.zck")

	cmd := exec.Command(zck, "-o", zckPath, src)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zck failed: %v\n%s", err, out)
	}

	f, err := os.Open(zckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got := extractZck(t, f)
	if !bytes.Equal(got, orig) {
		t.Fatalf("extracted content differs from original (got %d bytes, want %d)", len(got), len(orig))
	}
}

// writeOurZck builds a zchunk file from content using fixed-size chunks, an
// empty dictionary, zstd compression and SHA-256, and writes it to path.
func writeOurZck(t *testing.T, path string, content []byte) {
	t.Helper()
	const chunkSize = 16 * 1024

	idx := &Index{ChunkChecksumType: SHA256}
	// Chunk 0 is the (empty) dictionary: zero lengths, all-zero digest.
	idx.Chunks = append(idx.Chunks, IndexEntry{Digest: make([]byte, 32)})

	var body []byte
	for off := 0; off < len(content); off += chunkSize {
		end := off + chunkSize
		if end > len(content) {
			end = len(content)
		}
		plain := content[off:end]
		comp, err := CompressChunk(CompressionZstd, nil, plain)
		if err != nil {
			t.Fatalf("CompressChunk: %v", err)
		}
		digest, err := SHA256.Sum(comp)
		if err != nil {
			t.Fatalf("Sum: %v", err)
		}
		idx.Chunks = append(idx.Chunks, IndexEntry{
			Digest:     digest,
			CompLength: uint64(len(comp)),
			Length:     uint64(len(plain)),
		})
		body = append(body, comp...)
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pre := &Preface{CompressionType: CompressionZstd}
	if _, err := WriteFile(f, SHA256, pre, idx, nil, body); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestCompatOurFileToUnzck builds a zchunk file with our writer/codec and asks
// the C `unzck` tool to decompress it — byte-identical output is the pass
// condition, exercising our write path against the reference reader.
func TestCompatOurFileToUnzck(t *testing.T) {
	unzck := lookTool(t, "unzck")

	dir := t.TempDir()
	orig := randBytes(t, 200*1024)
	zckPath := filepath.Join(dir, "ours.zck")
	writeOurZck(t, zckPath, orig)

	cmd := exec.Command(unzck, "-c", zckPath)
	cmd.Dir = dir
	got, err := cmd.Output()
	if err != nil {
		t.Fatalf("unzck failed: %v", err)
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("unzck output differs from original (got %d bytes, want %d)", len(got), len(orig))
	}
}

// BenchmarkCompatExtract compares our in-process Extract against the C `unzck`
// tool decompressing the same zck-produced file. The "go" sub-benchmark times
// pure in-process decode; the "unzck" sub-benchmark times the reference and
// therefore includes process-spawn overhead — the realistic cost of shelling
// out to the C tool, not a pure codec comparison.
//
// Run with: go test -tags=compat -bench BenchmarkCompatExtract -run '^$' ./internal/zchunk/
func BenchmarkCompatExtract(b *testing.B) {
	zck := lookTool(b, "zck")
	unzck := lookTool(b, "unzck")

	dir := b.TempDir()
	orig := benchData(4 << 20) // 4 MiB of compressible content
	src := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(src, orig, 0o644); err != nil {
		b.Fatal(err)
	}
	zckPath := filepath.Join(dir, "data.zck")
	if out, err := exec.Command(zck, "-o", zckPath, src).CombinedOutput(); err != nil {
		b.Fatalf("zck failed: %v\n%s", err, out)
	}
	zckBytes, err := os.ReadFile(zckPath)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("go", func(b *testing.B) {
		b.SetBytes(int64(len(orig)))
		for i := 0; i < b.N; i++ {
			r := bytes.NewReader(zckBytes)
			lead, err := ReadLead(r)
			if err != nil {
				b.Fatal(err)
			}
			pre, err := ReadPreface(r, lead.ChecksumType)
			if err != nil {
				b.Fatal(err)
			}
			idx, err := ReadIndex(r, pre.UncompressedSource())
			if err != nil {
				b.Fatal(err)
			}
			if _, err := ReadSignatures(r); err != nil {
				b.Fatal(err)
			}
			if _, err := idx.Extract(r, pre.CompressionType, io.Discard); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("unzck", func(b *testing.B) {
		b.SetBytes(int64(len(orig)))
		for i := 0; i < b.N; i++ {
			cmd := exec.Command(unzck, "-c", zckPath)
			cmd.Stdout = io.Discard
			if err := cmd.Run(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
