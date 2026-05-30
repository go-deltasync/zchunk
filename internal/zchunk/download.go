package zchunk

import (
	"bytes"
	"fmt"
	"io"
)

// RangeReader fetches a byte range from a remote target body. offset is relative
// to the start of the body (the dictionary chunk is at offset 0) and length is
// the number of bytes requested; an implementation must return exactly length
// bytes or a non-nil error.
type RangeReader interface {
	ReadRange(offset, length int64) ([]byte, error)
}

// AssembleBody reconstructs the target file's body and writes it to out. For
// each target chunk the plan says whether to copy the compressed bytes from the
// local body (local, a ReaderAt over the local file's body) or fetch them from
// remote. Every non-empty chunk is verified against its index digest (over the
// compressed bytes), so corruption in either source is caught. It returns the
// number of body bytes written. The plan must have been produced from target.
func (p *DeltaPlan) AssembleBody(target *Index, local io.ReaderAt, remote RangeReader, out io.Writer) (int64, error) {
	// Validate the chunk checksum type once so per-chunk hashing cannot fail.
	if _, err := target.ChunkChecksumType.Size(); err != nil {
		return 0, err
	}

	var written int64
	for i, s := range p.Sources {
		if s.Length == 0 {
			continue // empty chunk: nothing to copy, fetch or write
		}

		var data []byte
		if s.Local {
			data = make([]byte, s.Length)
			n, err := local.ReadAt(data, int64(s.Offset))
			if n != int(s.Length) {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return written, fmt.Errorf("zchunk: read local chunk %d: %w", i, err)
			}
		} else {
			var err error
			data, err = remote.ReadRange(int64(s.Offset), int64(s.Length))
			if err != nil {
				return written, fmt.Errorf("zchunk: fetch chunk %d: %w", i, err)
			}
			if int64(len(data)) != int64(s.Length) {
				return written, fmt.Errorf("zchunk: fetch chunk %d: got %d bytes, want %d",
					i, len(data), s.Length)
			}
		}

		// Type is pre-validated, so Sum cannot error here.
		sum, _ := target.ChunkChecksumType.Sum(data)
		if !bytes.Equal(sum, target.Chunks[i].Digest) {
			return written, fmt.Errorf("zchunk: chunk %d digest mismatch", i)
		}

		n, err := out.Write(data)
		written += int64(n)
		if err != nil {
			return written, fmt.Errorf("zchunk: write chunk %d: %w", i, err)
		}
	}
	return written, nil
}
