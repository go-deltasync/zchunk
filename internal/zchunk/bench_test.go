package zchunk

import (
	"bytes"
	"io"
	"math/rand"
	"testing"
)

// benchData returns n bytes of moderately compressible content (the realistic
// case for repository metadata: structured, repetitive text), generated
// deterministically so successive benchmark runs are comparable.
func benchData(n int) []byte {
	rng := rand.New(rand.NewSource(1))
	tokens := [][]byte{
		[]byte("name=package-"), []byte(" version=1.2."), []byte(" arch=x86_64"),
		[]byte(" requires=libfoo "), []byte("\n<entry id=\""), []byte("\"/>\n"),
		[]byte(" summary=\"a useful tool\" "), []byte("license=BSD "),
	}
	buf := make([]byte, 0, n+32)
	for len(buf) < n {
		buf = append(buf, tokens[rng.Intn(len(tokens))]...)
	}
	return buf[:n]
}

// benchChunks splits b into fixed-size chunks for building a multi-chunk file.
func benchChunks(b []byte, chunkSize int) [][]byte {
	var out [][]byte
	for off := 0; off < len(b); off += chunkSize {
		end := off + chunkSize
		if end > len(b) {
			end = len(b)
		}
		out = append(out, b[off:end])
	}
	return out
}

// buildBenchBody mirrors buildBody without a *testing.T, for benchmarks: chunk 0
// is an empty dict, data chunks are zstd-compressed independently.
func buildBenchBody(b *testing.B, ck ChecksumType, dataPlain [][]byte) (*Index, []byte) {
	b.Helper()
	size, err := ck.Size()
	if err != nil {
		b.Fatal(err)
	}
	idx := &Index{ChunkChecksumType: ck, Chunks: []IndexEntry{{Digest: make([]byte, size)}}}
	var body bytes.Buffer
	for _, plain := range dataPlain {
		comp, err := CompressChunk(CompressionZstd, nil, plain)
		if err != nil {
			b.Fatal(err)
		}
		digest, err := ck.Sum(comp)
		if err != nil {
			b.Fatal(err)
		}
		idx.Chunks = append(idx.Chunks, IndexEntry{
			Digest: digest, CompLength: uint64(len(comp)), Length: uint64(len(plain)),
		})
		body.Write(comp)
	}
	return idx, body.Bytes()
}

func BenchmarkCompressChunkZstd(b *testing.B) {
	data := benchData(64 * 1024)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := CompressChunk(CompressionZstd, nil, data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecompressChunkZstd(b *testing.B) {
	data := benchData(64 * 1024)
	comp, err := CompressChunk(CompressionZstd, nil, data)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecompressChunk(CompressionZstd, nil, comp, uint64(len(data))); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChecksumSHA256(b *testing.B) {
	data := benchData(64 * 1024)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SHA256.Sum(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompressedIntRoundTrip(b *testing.B) {
	vals := []uint64{0, 127, 128, 300, 1 << 20, 1 << 40, ^uint64(0)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, v := range vals {
			enc := AppendCompressedInt(nil, v)
			if _, err := ReadCompressedInt(bytes.NewReader(enc)); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkExtract(b *testing.B) {
	data := benchData(1 << 20) // 1 MiB
	idx, body := buildBenchBody(b, SHA256, benchChunks(data, 32*1024))
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := idx.Extract(bytes.NewReader(body), CompressionZstd, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteFile(b *testing.B) {
	data := benchData(1 << 20)
	idx, body := buildBenchBody(b, SHA256, benchChunks(data, 32*1024))
	pre := &Preface{CompressionType: CompressionZstd}
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := WriteFile(io.Discard, SHA256, pre, idx, nil, body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBuildBody contrasts two ways of compressing a whole file's worth of
// chunks: "per-chunk" constructs a fresh zstd encoder for every chunk (the old
// CompressChunk-in-a-loop pattern), while "builder" reuses one encoder across
// all chunks via Builder. The allocs/op gap is the encoder-reuse win.
func BenchmarkBuildBody(b *testing.B) {
	chunks := benchChunks(benchData(1<<20), 32*1024)

	b.Run("per-chunk", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, plain := range chunks {
				if _, err := CompressChunk(CompressionZstd, nil, plain); err != nil {
					b.Fatal(err)
				}
			}
		}
	})

	b.Run("builder", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			bld, err := NewBuilder(CompressionZstd, SHA256, nil)
			if err != nil {
				b.Fatal(err)
			}
			for _, plain := range chunks {
				bld.AddChunk(plain)
			}
			bld.Close()
		}
	})
}

func BenchmarkPlanDelta(b *testing.B) {
	data := benchData(4 << 20)
	idx, _ := buildBenchBody(b, SHA256, benchChunks(data, 16*1024))
	// Local copy shares every other chunk.
	local := &Index{ChunkChecksumType: SHA256}
	for i, c := range idx.Chunks {
		if i%2 == 0 {
			local.Chunks = append(local.Chunks, c)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := PlanDelta(local, idx); err != nil {
			b.Fatal(err)
		}
	}
}
