package zchunk

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// makeSamples builds a corpus of n similar samples by repeating a shared,
// structured prefix (so dmers recur across samples and the selector has real
// content to pick) followed by per-sample noise.
func makeSamples(t *testing.T, n, size int) [][]byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	// A shared block that recurs across every sample, longer than defaultSegLen
	// so the sliding window in bestSegment exercises its inner-slide branch.
	shared := make([]byte, defaultSegLen*4)
	for i := range shared {
		shared[i] = byte(i*7 + 3)
	}
	samples := make([][]byte, n)
	for k := range samples {
		s := make([]byte, 0, size)
		s = append(s, shared...)
		for len(s) < size {
			s = append(s, byte(rng.Intn(256)))
		}
		samples[k] = s[:size]
	}
	return samples
}

func TestGenerateDictRoundTrip(t *testing.T) {
	samples := makeSamples(t, 8, defaultSegLen*8)
	dict, err := GenerateDict(samples, 4096)
	if err != nil {
		t.Fatalf("GenerateDict: %v", err)
	}
	if len(dict) == 0 {
		t.Fatal("GenerateDict returned an empty dictionary")
	}

	// It must be a real zstd dictionary the codec accepts.
	if _, err := zstd.InspectDictionary(dict); err != nil {
		t.Fatalf("InspectDictionary: %v", err)
	}

	// And it must drive a compress/decompress round-trip.
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderDict(dict))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer enc.Close()
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDicts(dict))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer dec.Close()

	for i, s := range samples {
		comp := enc.EncodeAll(s, nil)
		got, err := dec.DecodeAll(comp, nil)
		if err != nil {
			t.Fatalf("sample %d DecodeAll: %v", i, err)
		}
		if !bytes.Equal(got, s) {
			t.Fatalf("sample %d round-trip mismatch", i)
		}
	}
}

func TestSelectDictContentCapsAtTarget(t *testing.T) {
	samples := makeSamples(t, 8, defaultSegLen*8)
	// A target that is not a multiple of segLen forces the in-segment cap branch
	// (len(seg) > room) on the final pick, and the result must never exceed it.
	const target = defaultSegLen*2 + 44
	content := selectDictContent(samples, target, defaultSegLen)
	if len(content) == 0 {
		t.Fatal("expected some selected content")
	}
	if len(content) > target {
		t.Fatalf("content %d exceeds target %d", len(content), target)
	}
}

func TestGenerateDictTooSmall(t *testing.T) {
	// All samples shorter than dmerLen → no dmer at all → empty content →
	// BuildDict fails, and GenerateDict propagates that error.
	samples := [][]byte{[]byte("abc"), []byte("de"), {}}
	if _, err := GenerateDict(samples, 4096); err == nil {
		t.Fatal("expected an error for samples too small to yield content")
	}
}

func TestSelectDictContentEmpty(t *testing.T) {
	// No sample reaches dmerLen, so freq stays empty and the selector bails.
	if got := selectDictContent([][]byte{[]byte("abc")}, 4096, defaultSegLen); got != nil {
		t.Fatalf("want nil content, got %d bytes", len(got))
	}
}

func TestSelectDictContentExhausts(t *testing.T) {
	// A tiny repetitive corpus has few distinct dmers; with a large target the
	// selector retires them all and breaks on a zero-score segment before the
	// target is reached.
	block := bytes.Repeat([]byte("0123456789ABCDEF"), 4) // 64 bytes, well-structured
	samples := [][]byte{block, block}
	content := selectDictContent(samples, 1<<20, defaultSegLen)
	if len(content) == 0 {
		t.Fatal("expected some selected content")
	}
	if len(content) >= 1<<20 {
		t.Fatal("selector should have stopped well before the huge target")
	}
}

func TestSelectDictContentTailOrder(t *testing.T) {
	// The first (best) pick must end up at the tail (nearest the data). Build a
	// corpus where one segment is unambiguously the most frequent, then check it
	// lands at the end of the selected content.
	hot := make([]byte, defaultSegLen)
	for i := range hot {
		hot[i] = byte(i)
	}
	cold := make([]byte, defaultSegLen)
	for i := range cold {
		cold[i] = byte(255 - i)
	}
	// hot appears in every sample; cold in only one.
	samples := [][]byte{
		append(append([]byte(nil), hot...), cold...),
		append([]byte(nil), hot...),
		append([]byte(nil), hot...),
	}
	content := selectDictContent(samples, 4096, defaultSegLen)
	if len(content) < len(hot) {
		t.Fatalf("content too short: %d", len(content))
	}
	tail := content[len(content)-len(hot):]
	if !bytes.Equal(tail, hot) {
		t.Fatal("the most valuable segment is not at the tail")
	}
}

func TestBestSegmentSkipsShortSamples(t *testing.T) {
	freq := map[uint64]uint32{}
	long := make([]byte, defaultSegLen)
	for i := range long {
		long[i] = byte(i)
	}
	for i := 0; i+dmerLen <= len(long); i++ {
		freq[binary.LittleEndian.Uint64(long[i:])]++
	}
	// A sample shorter than dmerLen must be skipped entirely; a sample between
	// dmerLen and segLen must be scored with a shortened window.
	short := []byte("xy")   // < dmerLen, skipped
	mid := long[:dmerLen+4] // dmerLen..segLen, window = len(mid)
	seg, score := bestSegment([][]byte{short, mid, long}, freq, defaultSegLen)
	if score == 0 || len(seg) == 0 {
		t.Fatal("expected a positive-scoring segment")
	}
	// The full-length sample should win over the shortened mid window.
	if len(seg) != defaultSegLen {
		t.Fatalf("want full-length winning segment, got %d", len(seg))
	}
}
