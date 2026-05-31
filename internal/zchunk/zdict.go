package zchunk

import (
	"encoding/binary"
	"hash/fnv"

	"github.com/klauspost/compress/zstd"
)

// A zstd dictionary improves compression — and therefore delta reuse — of a set
// of similar files (such as successive versions of repository metadata) by
// seeding the compressor with content common to them. The reference's
// zck_gen_zdict produces one by extracting a file's chunks and shelling out to
// `zstd --train`; we stay cgo-free and self-contained by implementing the
// content selection ourselves (a fastCover-style selector) and building the
// final dictionary with the pure-Go klauspost zstd dictionary builder.

const (
	// dmerLen is the length of the substrings ("dmers") whose frequencies drive
	// segment scoring. zstd's COVER uses 6 or 8; 8 lets a dmer be read as one
	// little-endian uint64, so it is both fast and exact (no hash collisions).
	dmerLen = 8
	// defaultSegLen is the candidate-segment length scored as a unit. zstd's k
	// is tuned per corpus; a fixed mid-range value keeps the selector simple
	// while still capturing repeated runs.
	defaultSegLen = 256
)

// GenerateDict trains a standard zstd dictionary from samples (for zchunk, the
// decompressed chunks of a file) and returns its serialised bytes — the same
// artifact `zstd --train` / zck_gen_zdict produce, usable by `zstd -D` and
// `zck --dict`. maxDictSize bounds the dictionary's content in bytes.
//
// Content selection is a fastCover-style greedy selector: it scores fixed-length
// segments by the combined frequency of the distinct dmers they contain, picks
// the best, retires those dmers and repeats until the content reaches maxDictSize
// or no useful segment remains. The selected content is then finalised into a
// real zstd dictionary (entropy tables and repeat offsets) by the pure-Go
// builder. It fails (via the builder) when the samples are too small to yield any
// dictionary content.
func GenerateDict(samples [][]byte, maxDictSize int) ([]byte, error) {
	content := selectDictContent(samples, maxDictSize, defaultSegLen)
	return zstd.BuildDict(zstd.BuildDictOptions{
		ID:       dictID(content),
		Contents: samples,
		History:  content,
		// Seed the dictionary's three repeat offsets with zstd's standard
		// defaults. Left zero, BuildDict emits offset 0, which is invalid and
		// makes the encoder reject the dictionary ("invalid offset"); {1,4,8}
		// keeps even small dictionaries loadable.
		Offsets: [3]int{1, 4, 8},
	})
}

// dictID derives a deterministic non-zero 32-bit dictionary ID from the chosen
// content. A real zstd dictionary must carry a non-zero ID (zstd --train picks a
// random one); deriving it from the content keeps the output reproducible while
// still distinguishing dictionaries built from different corpora.
func dictID(content []byte) uint32 {
	h := fnv.New32a()
	h.Write(content)
	// OR in the low bit so the ID is always odd, hence never zero, without a
	// branch that the 100 %-coverage gate would flag as unreachable.
	return h.Sum32() | 1
}

// selectDictContent runs the fastCover-style greedy selection and returns the
// chosen dictionary content (most valuable segment last, nearest the data, as
// zstd places it). It returns nil when the samples contain no dmer at all.
func selectDictContent(samples [][]byte, targetSize, segLen int) []byte {
	// Count every dmer's frequency across all samples.
	freq := map[uint64]uint32{}
	for _, s := range samples {
		for i := 0; i+dmerLen <= len(s); i++ {
			freq[binary.LittleEndian.Uint64(s[i:])]++
		}
	}
	if len(freq) == 0 {
		return nil
	}

	var content []byte
	for len(content) < targetSize {
		seg, score := bestSegment(samples, freq, segLen)
		if score == 0 {
			break // no remaining segment adds value
		}
		// Retire the consumed dmers before trimming, so overlapping regions are
		// not re-selected even when the segment is capped to the remaining room.
		for i := 0; i+dmerLen <= len(seg); i++ {
			freq[binary.LittleEndian.Uint64(seg[i:])] = 0
		}
		if room := targetSize - len(content); len(seg) > room {
			seg = seg[:room]
		}
		// Prepend so the first (best) pick ends up at the tail, nearest the data.
		content = append(append([]byte(nil), seg...), content...)
	}
	return content
}

// bestSegment returns the highest-scoring length-segLen window across all
// samples, where a window's score is the sum of freq over the distinct dmers it
// contains. A window is shortened to a sample that is itself shorter than segLen.
func bestSegment(samples [][]byte, freq map[uint64]uint32, segLen int) ([]byte, uint32) {
	var best []byte
	var bestScore uint32
	for _, s := range samples {
		if len(s) < dmerLen {
			continue
		}
		w := segLen
		if w > len(s) {
			w = len(s)
		}

		// Score the first window [0, w) and seed the active-dmer multiset.
		active := map[uint64]int{}
		var score uint32
		for i := 0; i+dmerLen <= w; i++ {
			k := binary.LittleEndian.Uint64(s[i:])
			if active[k] == 0 {
				score += freq[k]
			}
			active[k]++
		}
		if score > bestScore {
			bestScore, best = score, s[0:w]
		}

		// Slide the window one byte at a time, updating the score incrementally.
		for start := 1; start+w <= len(s); start++ {
			out := binary.LittleEndian.Uint64(s[start-1:])
			active[out]--
			if active[out] == 0 {
				score -= freq[out]
			}
			in := binary.LittleEndian.Uint64(s[start+w-dmerLen:])
			if active[in] == 0 {
				score += freq[in]
			}
			active[in]++
			if score > bestScore {
				bestScore, best = score, s[start:start+w]
			}
		}
	}
	return best, bestScore
}
