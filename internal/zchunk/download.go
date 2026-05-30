package zchunk

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// RangeReader fetches a byte range from a remote target body. offset is relative
// to the start of the body (the dictionary chunk is at offset 0) and length is
// the number of bytes requested; an implementation must return exactly length
// bytes or a non-nil error. Because AssembleBody fetches ranges concurrently, a
// RangeReader must be safe for concurrent use by multiple goroutines.
type RangeReader interface {
	ReadRange(offset, length int64) ([]byte, error)
}

// defaultFetchConcurrency bounds how many remote range requests AssembleBody has
// in flight at once.
const defaultFetchConcurrency = 4

// remoteRun is a maximal run of consecutive must-fetch chunks. Since every
// target chunk immediately follows the previous one in the body, such a run is a
// single contiguous byte range: it is fetched in one request and split back into
// its constituent chunks, cutting the number of round-trips.
type remoteRun struct {
	offset int64 // body offset of the run's first chunk
	length int64 // total compressed length of the run
	first  int   // index into Sources of the run's first chunk
	count  int   // number of chunks in the run
}

// runResult is one run's fetched bytes or the error that fetching it produced.
type runResult struct {
	data []byte
	err  error
}

// AssembleBody reconstructs the target file's body and writes it to out. For
// each target chunk the plan says whether to copy the compressed bytes from the
// local body (local, a ReaderAt over the local file's body) or fetch them from
// remote. Consecutive must-fetch chunks are coalesced into a single range
// request, and the runs are fetched concurrently (bounded) while output is
// still written strictly in order. Every non-empty chunk is verified against
// its index digest (over the compressed bytes), so corruption in either source
// is caught. It returns the number of body bytes written. The plan must have
// been produced from target.
func (p *DeltaPlan) AssembleBody(target *Index, local io.ReaderAt, remote RangeReader, out io.Writer) (int64, error) {
	// Validate the chunk checksum type once so per-chunk hashing cannot fail.
	if _, err := target.ChunkChecksumType.Size(); err != nil {
		return 0, err
	}

	runs := p.remoteRuns()

	// Fetch every run concurrently, bounded by a semaphore. Results are buffered
	// per run and consumed in source order below, so the output stays ordered.
	results := make([]runResult, len(runs))
	ready := make([]chan struct{}, len(runs))
	for k := range ready {
		ready[k] = make(chan struct{})
	}
	sem := make(chan struct{}, defaultFetchConcurrency)
	var wg sync.WaitGroup
	for k := range runs {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := remote.ReadRange(runs[k].offset, runs[k].length)
			if err == nil && int64(len(data)) != runs[k].length {
				err = fmt.Errorf("got %d bytes, want %d", len(data), runs[k].length)
			}
			results[k] = runResult{data: data, err: err}
			close(ready[k])
		}(k)
	}
	// Wait for all workers before returning, even on an early error, so none
	// outlives the call.
	defer wg.Wait()

	runAt := make(map[int]int, len(runs)) // Sources index of run start -> run number
	for k, r := range runs {
		runAt[r.first] = k
	}

	var written int64
	var localBuf []byte // reused local-read scratch, grown to the largest local chunk
	for i := 0; i < len(p.Sources); {
		s := p.Sources[i]
		switch {
		case s.Length == 0:
			// Empty chunk: nothing to copy, fetch or write.
			i++
		case s.Local:
			if uint64(cap(localBuf)) >= s.Length {
				localBuf = localBuf[:s.Length]
			} else {
				localBuf = make([]byte, s.Length)
			}
			data := localBuf
			n, err := local.ReadAt(data, int64(s.Offset))
			if n != int(s.Length) {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return written, fmt.Errorf("zchunk: read local chunk %d: %w", i, err)
			}
			n, err = verifyWriteChunk(target, i, data, out)
			written += int64(n)
			if err != nil {
				return written, err
			}
			i++
		default:
			// Start of a remote run: wait for its fetch, then split and write
			// each chunk in order.
			k := runAt[i]
			<-ready[k]
			if results[k].err != nil {
				return written, fmt.Errorf("zchunk: fetch chunk %d: %w", i, results[k].err)
			}
			var pos int64
			for c := 0; c < runs[k].count; c++ {
				idx := i + c
				length := int64(p.Sources[idx].Length)
				data := results[k].data[pos : pos+length]
				pos += length
				n, err := verifyWriteChunk(target, idx, data, out)
				written += int64(n)
				if err != nil {
					return written, err
				}
			}
			i += runs[k].count
		}
	}
	return written, nil
}

// remoteRuns groups the plan's must-fetch chunks into maximal contiguous runs.
func (p *DeltaPlan) remoteRuns() []remoteRun {
	var runs []remoteRun
	for i := 0; i < len(p.Sources); {
		s := p.Sources[i]
		if s.Local || s.Length == 0 {
			i++
			continue
		}
		run := remoteRun{offset: int64(s.Offset), first: i}
		for i < len(p.Sources) && !p.Sources[i].Local && p.Sources[i].Length > 0 {
			run.length += int64(p.Sources[i].Length)
			run.count++
			i++
		}
		runs = append(runs, run)
	}
	return runs
}

// verifyWriteChunk checks data against chunk idx's index digest and, on success,
// writes it to out. It returns the number of bytes written. The chunk checksum
// type must already be validated.
func verifyWriteChunk(target *Index, idx int, data []byte, out io.Writer) (int, error) {
	sum, _ := target.ChunkChecksumType.Sum(data)
	if !bytes.Equal(sum, target.Chunks[idx].Digest) {
		return 0, fmt.Errorf("zchunk: chunk %d digest mismatch", idx)
	}
	n, err := out.Write(data)
	if err != nil {
		return n, fmt.Errorf("zchunk: write chunk %d: %w", idx, err)
	}
	return n, nil
}
