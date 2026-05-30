package zchunk

import "testing"

// dig builds a distinct SHA-256-sized digest from a single seed byte.
func dig(seed byte) []byte {
	d := make([]byte, 32)
	for i := range d {
		d[i] = seed
	}
	return d
}

func TestPlanDelta(t *testing.T) {
	// Local body: [dict(empty)] [A:100] [B:50] [A-dup:100].
	local := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: make([]byte, 32), CompLength: 0, Length: 0},
		{Digest: dig(0xAA), CompLength: 100, Length: 200},
		{Digest: dig(0xBB), CompLength: 50, Length: 80},
		{Digest: dig(0xAA), CompLength: 100, Length: 200}, // duplicate of A
	}}
	// Target body: [dict(empty)] [A:100] [C:70(new)] [B:50].
	target := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: make([]byte, 32), CompLength: 0, Length: 0},
		{Digest: dig(0xAA), CompLength: 100, Length: 200},
		{Digest: dig(0xCC), CompLength: 70, Length: 120},
		{Digest: dig(0xBB), CompLength: 50, Length: 80},
	}}

	plan, err := PlanDelta(local, target)
	if err != nil {
		t.Fatalf("PlanDelta: %v", err)
	}
	if len(plan.Sources) != 4 {
		t.Fatalf("got %d sources, want 4", len(plan.Sources))
	}

	// Chunk 0: empty -> local, zero length.
	if s := plan.Sources[0]; !s.Local || s.Length != 0 {
		t.Fatalf("chunk0 = %+v, want local empty", s)
	}
	// Chunk 1: A present locally at offset 0 (first occurrence wins).
	if s := plan.Sources[1]; !s.Local || s.Offset != 0 || s.Length != 100 {
		t.Fatalf("chunk1 = %+v, want local off=0 len=100", s)
	}
	// Chunk 2: C is new -> remote, at its target body offset (0+100=100).
	if s := plan.Sources[2]; s.Local || s.Offset != 100 || s.Length != 70 {
		t.Fatalf("chunk2 = %+v, want remote off=100 len=70", s)
	}
	// Chunk 3: B present locally at offset 100 (after empty dict + A).
	if s := plan.Sources[3]; !s.Local || s.Offset != 100 || s.Length != 50 {
		t.Fatalf("chunk3 = %+v, want local off=100 len=50", s)
	}

	if got := plan.FetchBytes(); got != 70 {
		t.Fatalf("FetchBytes = %d, want 70", got)
	}
	if got := plan.ReuseBytes(); got != 150 {
		t.Fatalf("ReuseBytes = %d, want 150", got)
	}
}

func TestPlanDeltaNilLocal(t *testing.T) {
	target := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: make([]byte, 32), CompLength: 0, Length: 0},
		{Digest: dig(0xAA), CompLength: 100, Length: 200},
	}}
	plan, err := PlanDelta(nil, target)
	if err != nil {
		t.Fatalf("PlanDelta(nil): %v", err)
	}
	if plan.Sources[1].Local {
		t.Fatal("chunk1 should be remote when there is no local index")
	}
	if plan.FetchBytes() != 100 || plan.ReuseBytes() != 0 {
		t.Fatalf("fetch=%d reuse=%d, want 100/0", plan.FetchBytes(), plan.ReuseBytes())
	}
}

func TestPlanDeltaTypeMismatch(t *testing.T) {
	local := &Index{ChunkChecksumType: SHA1, Chunks: []IndexEntry{
		{Digest: make([]byte, 20), CompLength: 10, Length: 10},
	}}
	target := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: make([]byte, 32), CompLength: 10, Length: 10},
	}}
	if _, err := PlanDelta(local, target); err == nil {
		t.Fatal("expected chunk checksum type mismatch error")
	}
}
