package zchunk

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

// allRemoteSetup builds a target whose three data chunks (1..3) are all absent
// from the local copy, so PlanDelta marks them must-fetch. Since they are
// consecutive in the body they coalesce into a single contiguous range.
func allRemoteSetup(t *testing.T) (target, local *Index, targetBody []byte) {
	t.Helper()
	c1 := []byte("alpha-chunk-bytes")
	c2 := []byte("bravo-chunk-bytes-longer")
	c3 := []byte("charlie-chunk")
	targetBody = append(append(append([]byte(nil), c1...), c2...), c3...)
	target = &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: make([]byte, 32), CompLength: 0, Length: 0},
		{Digest: sum256(t, c1), CompLength: uint64(len(c1)), Length: 1},
		{Digest: sum256(t, c2), CompLength: uint64(len(c2)), Length: 1},
		{Digest: sum256(t, c3), CompLength: uint64(len(c3)), Length: 1},
	}}
	// Empty local index: nothing reusable.
	local = &Index{ChunkChecksumType: SHA256}
	return
}

// TestAssembleBodyCoalesced checks that a run of consecutive must-fetch chunks
// is fetched in ONE range request and split back into its constituent chunks.
func TestAssembleBodyCoalesced(t *testing.T) {
	target, local, targetBody := allRemoteSetup(t)
	plan, err := PlanDelta(local, target)
	if err != nil {
		t.Fatalf("PlanDelta: %v", err)
	}
	fr := &fakeRange{body: targetBody}
	var out bytes.Buffer
	n, err := plan.AssembleBody(target, bytes.NewReader(nil), fr, &out)
	if err != nil {
		t.Fatalf("AssembleBody: %v", err)
	}
	if int(n) != len(targetBody) || !bytes.Equal(out.Bytes(), targetBody) {
		t.Fatalf("assembled body mismatch (n=%d)", n)
	}
	if fr.calls != 1 {
		t.Fatalf("expected one coalesced range request, got %d", fr.calls)
	}
}

// TestAssembleBodyRunChunkError exercises the per-chunk error path inside the
// split loop of a multi-chunk remote run (the second chunk's digest is wrong).
func TestAssembleBodyRunChunkError(t *testing.T) {
	target, local, targetBody := allRemoteSetup(t)
	plan, err := PlanDelta(local, target)
	if err != nil {
		t.Fatalf("PlanDelta: %v", err)
	}
	bad := &Index{ChunkChecksumType: SHA256, Chunks: append([]IndexEntry(nil), target.Chunks...)}
	bad.Chunks[2].Digest = bytes.Repeat([]byte{0xff}, 32) // 2nd chunk of the run
	if _, err := plan.AssembleBody(bad, bytes.NewReader(nil), &fakeRange{body: targetBody}, io.Discard); err == nil {
		t.Fatal("expected digest mismatch within the run")
	}
}

// fakeRange serves byte ranges from an in-memory target body. It counts the
// number of ReadRange calls so tests can assert that consecutive must-fetch
// chunks are coalesced into a single request.
type fakeRange struct {
	body  []byte
	err   error // returned unconditionally when non-nil
	short bool  // when true, returns one fewer byte than requested
	mu    sync.Mutex
	calls int
}

func (f *fakeRange) ReadRange(offset, length int64) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.short {
		length--
	}
	return append([]byte(nil), f.body[offset:offset+length]...), nil
}

// shortReaderAt always reports a short read with a nil error.
type shortReaderAt struct{}

func (shortReaderAt) ReadAt(p []byte, off int64) (int, error) { return len(p) - 1, nil }

func sum256(t *testing.T, b []byte) []byte {
	t.Helper()
	d, err := SHA256.Sum(b)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	return d
}

// mixedSetup builds a target index/body where chunks 1 and 3 are also present in
// a local body (in a different order), chunk 2 is new, and chunk 0 is an empty
// dictionary.
func mixedSetup(t *testing.T) (target, local *Index, targetBody, localBody []byte) {
	t.Helper()
	c1 := []byte("first-chunk-compressed-bytes")
	c2 := []byte("second-new-chunk-bytes")
	c3 := []byte("third-chunk-compressed")

	targetBody = append(append(append([]byte(nil), c1...), c2...), c3...)
	target = &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: make([]byte, 32), CompLength: 0, Length: 0},
		{Digest: sum256(t, c1), CompLength: uint64(len(c1)), Length: 1},
		{Digest: sum256(t, c2), CompLength: uint64(len(c2)), Length: 1},
		{Digest: sum256(t, c3), CompLength: uint64(len(c3)), Length: 1},
	}}

	localBody = append(append([]byte(nil), c3...), c1...)
	local = &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
		{Digest: sum256(t, c3), CompLength: uint64(len(c3)), Length: 1},
		{Digest: sum256(t, c1), CompLength: uint64(len(c1)), Length: 1},
	}}
	return
}

func TestAssembleBody(t *testing.T) {
	target, local, targetBody, localBody := mixedSetup(t)
	plan, err := PlanDelta(local, target)
	if err != nil {
		t.Fatalf("PlanDelta: %v", err)
	}

	var out bytes.Buffer
	n, err := plan.AssembleBody(target, bytes.NewReader(localBody), &fakeRange{body: targetBody}, &out)
	if err != nil {
		t.Fatalf("AssembleBody: %v", err)
	}
	if int(n) != len(targetBody) || !bytes.Equal(out.Bytes(), targetBody) {
		t.Fatalf("assembled body mismatch (n=%d)", n)
	}
	// Sanity: chunk 2 was fetched, chunks 1 and 3 reused.
	if plan.FetchBytes() != uint64(len("second-new-chunk-bytes")) {
		t.Fatalf("FetchBytes = %d", plan.FetchBytes())
	}
}

func TestAssembleBodyBadChecksumType(t *testing.T) {
	target := &Index{ChunkChecksumType: 99, Chunks: []IndexEntry{{Digest: nil, CompLength: 1, Length: 1}}}
	plan := &DeltaPlan{Sources: []ChunkSource{{Index: 0, Length: 1}}}
	if _, err := plan.AssembleBody(target, bytes.NewReader(nil), &fakeRange{}, io.Discard); err == nil {
		t.Fatal("AssembleBody accepted an unknown chunk checksum type")
	}
}

func TestAssembleBodyErrors(t *testing.T) {
	target, local, targetBody, localBody := mixedSetup(t)
	plan, err := PlanDelta(local, target)
	if err != nil {
		t.Fatalf("PlanDelta: %v", err)
	}

	t.Run("local-read-error-eof", func(t *testing.T) {
		// Local body too short for the reused chunks.
		if _, err := plan.AssembleBody(target, bytes.NewReader([]byte("xx")), &fakeRange{body: targetBody}, io.Discard); err == nil {
			t.Fatal("expected local read error")
		}
	})

	t.Run("local-read-short-nil-err", func(t *testing.T) {
		if _, err := plan.AssembleBody(target, shortReaderAt{}, &fakeRange{body: targetBody}, io.Discard); err == nil {
			t.Fatal("expected local short-read error")
		}
	})

	t.Run("remote-fetch-error", func(t *testing.T) {
		if _, err := plan.AssembleBody(target, bytes.NewReader(localBody), &fakeRange{err: errBoom}, io.Discard); err == nil {
			t.Fatal("expected remote fetch error")
		}
	})

	t.Run("remote-short", func(t *testing.T) {
		if _, err := plan.AssembleBody(target, bytes.NewReader(localBody), &fakeRange{body: targetBody, short: true}, io.Discard); err == nil {
			t.Fatal("expected remote short-read error")
		}
	})

	t.Run("digest-mismatch", func(t *testing.T) {
		bad := &Index{ChunkChecksumType: SHA256, Chunks: append([]IndexEntry(nil), target.Chunks...)}
		bad.Chunks[1].Digest = bytes.Repeat([]byte{0xff}, 32)
		if _, err := plan.AssembleBody(bad, bytes.NewReader(localBody), &fakeRange{body: targetBody}, io.Discard); err == nil {
			t.Fatal("expected digest mismatch")
		}
	})

	t.Run("write-error", func(t *testing.T) {
		if _, err := plan.AssembleBody(target, bytes.NewReader(localBody), &fakeRange{body: targetBody}, &failWriter{failAt: 1}); err == nil {
			t.Fatal("expected write error")
		}
	})
}
