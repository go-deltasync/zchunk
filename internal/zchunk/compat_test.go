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

// readFullRange serves absolute byte ranges from an in-memory file, so a real
// .zck file can be fed to ReadRemoteHeader without a network server.
type readFullRange struct{ data []byte }

func (r readFullRange) ReadRange(offset, length int64) ([]byte, error) {
	end := offset + length
	if end > int64(len(r.data)) {
		end = int64(len(r.data))
	}
	return append([]byte(nil), r.data[offset:end]...), nil
}

// TestCompatZckHeaderVerifies confirms that a header produced by the C `zck`
// tool passes our embedded-checksum verification: ReadRemoteHeader recomputes
// the header digest over lead-without-checksum + header body and matches it
// against the lead, exercising the read-path integrity check against the
// reference's exact byte layout.
func TestCompatZckHeaderVerifies(t *testing.T) {
	zck := lookTool(t, "zck")

	dir := t.TempDir()
	orig := randBytes(t, 256*1024)
	src := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(src, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	zckPath := filepath.Join(dir, "data.zck")
	if out, err := exec.Command(zck, "-o", zckPath, src).CombinedOutput(); err != nil {
		t.Fatalf("zck failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(zckPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRemoteHeader(readFullRange{data: data}); err != nil {
		t.Fatalf("ReadRemoteHeader on zck output: %v", err)
	}
}

// writeOurZck builds a zchunk file from content using fixed-size chunks, an
// empty dictionary, zstd compression and SHA-256, and writes it to path.
func writeOurZck(t *testing.T, path string, content []byte) {
	t.Helper()
	const chunkSize = 16 * 1024

	// Use the high-level Builder (empty dictionary, zstd, SHA-256), which reuses
	// a single encoder across chunks — also exercising that path against unzck.
	b, err := NewBuilder(CompressionZstd, SHA256, nil)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	defer b.Close()
	for off := 0; off < len(content); off += chunkSize {
		end := off + chunkSize
		if end > len(content) {
			end = len(content)
		}
		b.AddChunk(content[off:end])
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pre := &Preface{CompressionType: CompressionZstd}
	if _, err := b.WriteFile(f, SHA256, pre, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestCompatOurDetachedHeaderToZckReadHeader builds a detached header with our
// writer and asks the C `zck_read_header` tool to parse and validate it,
// proving our "\0ZHR1" magic and the Magic-substituted header checksum match
// what the reference expects.
func TestCompatOurDetachedHeaderToZckReadHeader(t *testing.T) {
	zrh := lookTool(t, "zck_read_header")

	dir := t.TempDir()
	orig := randBytes(t, 120*1024)

	const chunkSize = 16 * 1024
	idx := &Index{ChunkChecksumType: SHA256}
	idx.Chunks = append(idx.Chunks, IndexEntry{Digest: make([]byte, 32)}) // empty dict
	var body []byte
	for off := 0; off < len(orig); off += chunkSize {
		end := off + chunkSize
		if end > len(orig) {
			end = len(orig)
		}
		comp, err := CompressChunk(CompressionZstd, nil, orig[off:end])
		if err != nil {
			t.Fatalf("CompressChunk: %v", err)
		}
		digest, err := SHA256.Sum(comp)
		if err != nil {
			t.Fatalf("Sum: %v", err)
		}
		idx.Chunks = append(idx.Chunks, IndexEntry{Digest: digest, CompLength: uint64(len(comp)), Length: uint64(end - off)})
		body = append(body, comp...)
	}

	hdrPath := filepath.Join(dir, "ours.zck.header")
	f, err := os.Create(hdrPath)
	if err != nil {
		t.Fatal(err)
	}
	pre := &Preface{CompressionType: CompressionZstd}
	if _, err := WriteDetachedHeader(f, SHA256, pre, idx, nil, body); err != nil {
		f.Close()
		t.Fatalf("WriteDetachedHeader: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(zrh, hdrPath).CombinedOutput()
	if err != nil {
		t.Fatalf("zck_read_header failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("detached header")) {
		t.Fatalf("zck_read_header did not recognise a detached header:\n%s", out)
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

// TestCompatGenZdictWithZstd trains a dictionary from a corpus of similar
// samples with our pure-Go GenerateDict, then asks the real `zstd` CLI to
// compress and decompress a sample with `-D ourdict`. A byte-identical
// round-trip proves we emit a standard zstd dictionary the reference toolchain
// accepts — the same artifact `zstd --train` / `zck_gen_zdict` produce.
func TestCompatGenZdictWithZstd(t *testing.T) {
	zstdCLI := lookTool(t, "zstd")

	// A shared, structured block recurs across every sample so training has real
	// content to select; each sample then diverges with its own random tail.
	shared := randBytes(t, 4096)
	const n, size = 8, 32 * 1024
	samples := make([][]byte, n)
	for k := range samples {
		s := append([]byte(nil), shared...)
		s = append(s, randBytes(t, size-len(shared))...)
		samples[k] = s
	}

	dict, err := GenerateDict(samples, 16*1024)
	if err != nil {
		t.Fatalf("GenerateDict: %v", err)
	}

	dir := t.TempDir()
	dictPath := filepath.Join(dir, "trained.dict")
	if err := os.WriteFile(dictPath, dict, 0o644); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "sample.bin")
	if err := os.WriteFile(srcPath, samples[0], 0o644); err != nil {
		t.Fatal(err)
	}
	zstPath := filepath.Join(dir, "sample.zst")
	outPath := filepath.Join(dir, "sample.out")

	if out, err := exec.Command(zstdCLI, "-q", "-f", "-D", dictPath, "-o", zstPath, srcPath).CombinedOutput(); err != nil {
		t.Fatalf("zstd compress with -D failed: %v\n%s", err, out)
	}
	if out, err := exec.Command(zstdCLI, "-q", "-f", "-d", "-D", dictPath, "-o", outPath, zstPath).CombinedOutput(); err != nil {
		t.Fatalf("zstd decompress with -D failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, samples[0]) {
		t.Fatalf("round-trip through zstd -D differs (got %d bytes, want %d)", len(got), len(samples[0]))
	}
}

// dictCorpus builds content with a recurring shared block (so a trained
// dictionary applies) and returns it split into fixed-size samples for training.
func dictCorpus(t *testing.T) (orig []byte, samples [][]byte) {
	t.Helper()
	shared := randBytes(t, 8*1024)
	for i := 0; i < 40; i++ {
		orig = append(orig, shared...)
		orig = append(orig, randBytes(t, 256)...)
	}
	const chunkSize = 8 * 1024
	for off := 0; off < len(orig); off += chunkSize {
		end := off + chunkSize
		if end > len(orig) {
			end = len(orig)
		}
		samples = append(samples, orig[off:end])
	}
	return orig, samples
}

// TestCompatDictZckToOurExtract trains a dictionary with our GenerateDict, has
// the C `zck -D` tool build a dict-based file from it, then verifies our reader +
// Extract reproduce the original — proving we decode the reference's structured
// (trained-dictionary) frames, whose blocks reference the dictionary's ID.
func TestCompatDictZckToOurExtract(t *testing.T) {
	zck := lookTool(t, "zck")

	dir := t.TempDir()
	orig, samples := dictCorpus(t)
	dict, err := GenerateDict(samples, 16*1024)
	if err != nil {
		t.Fatalf("GenerateDict: %v", err)
	}
	dictPath := filepath.Join(dir, "trained.dict")
	if err := os.WriteFile(dictPath, dict, 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(src, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	zckPath := filepath.Join(dir, "data.zck")
	if out, err := exec.Command(zck, "-D", dictPath, "-o", zckPath, src).CombinedOutput(); err != nil {
		t.Fatalf("zck -D failed: %v\n%s", err, out)
	}

	f, err := os.Open(zckPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := extractZck(t, f); !bytes.Equal(got, orig) {
		t.Fatalf("dict-based zck file extracted incorrectly (got %d bytes, want %d)", len(got), len(orig))
	}
}

// TestCompatOurDictFileToUnzck builds a dict-based file with our Builder (trained
// dictionary as chunk 0, the structured artifact loaded in zstd auto mode) and
// asks the C `unzck` to decompress it — byte-identical output proves our trained
// dictionaries and frames are wire-compatible with the reference reader.
func TestCompatOurDictFileToUnzck(t *testing.T) {
	unzck := lookTool(t, "unzck")

	dir := t.TempDir()
	orig, samples := dictCorpus(t)
	dict, err := GenerateDict(samples, 16*1024)
	if err != nil {
		t.Fatalf("GenerateDict: %v", err)
	}
	b, err := NewBuilder(CompressionZstd, SHA256, dict)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	defer b.Close()
	for _, s := range samples {
		b.AddChunk(s)
	}

	zckPath := filepath.Join(dir, "ours.zck")
	f, err := os.Create(zckPath)
	if err != nil {
		t.Fatal(err)
	}
	pre := &Preface{CompressionType: CompressionZstd}
	if _, err := b.WriteFile(f, SHA256, pre, nil); err != nil {
		f.Close()
		t.Fatalf("WriteFile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := exec.Command(unzck, "-c", zckPath).Output()
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
