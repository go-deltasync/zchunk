package zchunk

import "fmt"

// ChunkSource describes where one target chunk's compressed bytes come from when
// reconstructing a target zchunk file from a local copy plus remote fetches.
type ChunkSource struct {
	// Index is the chunk's position in the target index.
	Index int
	// Local is true when the chunk's compressed bytes are already present in the
	// local file (a byte-identical chunk, matched by compressed digest) and can
	// be copied instead of downloaded. Empty chunks (zero length) are always
	// local: there is nothing to fetch.
	Local bool
	// Offset is the byte offset of the compressed chunk within its source body
	// (the local body when Local, otherwise the target/remote body), relative to
	// the start of the body (the first chunk, the dictionary, is at offset 0).
	Offset uint64
	// Length is the compressed length of the chunk.
	Length uint64
}

// DeltaPlan lists, for every chunk of a target file, where to obtain it.
type DeltaPlan struct {
	// Sources has one entry per target chunk, in order.
	Sources []ChunkSource
}

// PlanDelta computes how to assemble target from a local file: each target chunk
// is taken from local when a byte-identical chunk (same compressed digest) is
// present there, and otherwise fetched from the remote target body. local may be
// nil or empty (then every non-empty chunk must be fetched). When local has any
// chunks, its chunk checksum type must match target's, since chunks are matched
// by digest.
func PlanDelta(local, target *Index) (*DeltaPlan, error) {
	if local != nil && len(local.Chunks) > 0 &&
		local.ChunkChecksumType != target.ChunkChecksumType {
		return nil, fmt.Errorf("zchunk: chunk checksum type mismatch: local %d, target %d",
			uint64(local.ChunkChecksumType), uint64(target.ChunkChecksumType))
	}

	// Map each local non-empty chunk's compressed digest to its body offset.
	localOffsets := map[string]uint64{}
	if local != nil {
		var off uint64
		for _, e := range local.Chunks {
			if e.CompLength > 0 {
				if _, ok := localOffsets[string(e.Digest)]; !ok {
					localOffsets[string(e.Digest)] = off
				}
			}
			off += e.CompLength
		}
	}

	plan := &DeltaPlan{Sources: make([]ChunkSource, len(target.Chunks))}
	var off uint64
	for i, e := range target.Chunks {
		src := ChunkSource{Index: i, Length: e.CompLength}
		switch {
		case e.CompLength == 0:
			// Empty chunk: nothing to copy or fetch.
			src.Local = true
		default:
			if loff, ok := localOffsets[string(e.Digest)]; ok {
				src.Local = true
				src.Offset = loff
			} else {
				src.Offset = off
			}
		}
		plan.Sources[i] = src
		off += e.CompLength
	}
	return plan, nil
}

// FetchBytes is the total compressed length of the chunks that must be
// downloaded from the remote body.
func (p *DeltaPlan) FetchBytes() uint64 {
	var n uint64
	for _, s := range p.Sources {
		if !s.Local {
			n += s.Length
		}
	}
	return n
}

// ReuseBytes is the total compressed length of the chunks reused from the local
// file.
func (p *DeltaPlan) ReuseBytes() uint64 {
	var n uint64
	for _, s := range p.Sources {
		if s.Local {
			n += s.Length
		}
	}
	return n
}
