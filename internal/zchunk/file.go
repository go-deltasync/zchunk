package zchunk

import (
	"bytes"
	"io"
)

// WriteFile assembles and writes a complete (non-detached) zchunk file to w:
// lead, preface, index, signatures and then body. It computes the two checksums
// the format derives from content:
//
//   - the preface's data checksum, taken over body with overallType. Per the
//     format, when the uncompressed-source flag is set the data checksum is not
//     generated and is written as zeros.
//   - the lead's header checksum, taken with overallType over everything from the
//     file start to the end of the signatures, excluding the header-checksum
//     field itself.
//
// overallType is the lead's checksum type (the format permits only SHA-1 or
// SHA-256 here, but any known type is accepted). body must be the already-built
// body: the compressed dictionary (chunk 0) followed by the compressed chunks,
// matching idx. pre.DataChecksum is ignored and recomputed; the caller's pre is
// not mutated. sigs may be nil, meaning a zero signature count.
func WriteFile(w io.Writer, overallType ChecksumType, pre *Preface, idx *Index,
	sigs *Signatures, body []byte) (int64, error) {
	digestSize, err := overallType.Size()
	if err != nil {
		return 0, err
	}
	if sigs == nil {
		sigs = &Signatures{}
	}

	// Data checksum over the body (zeros when the data checksum is suppressed).
	p := *pre
	if p.UncompressedSource() {
		p.DataChecksum = make([]byte, digestSize)
	} else {
		// overallType is validated above, so Sum cannot error here.
		p.DataChecksum, _ = overallType.Sum(body)
	}

	// Serialise preface, index and signatures into the contiguous header body.
	var hdr bytes.Buffer
	if _, err := p.WriteTo(&hdr); err != nil {
		return 0, err
	}
	if _, err := idx.WriteTo(&hdr, p.UncompressedSource()); err != nil {
		return 0, err
	}
	if _, err := sigs.WriteTo(&hdr); err != nil {
		return 0, err
	}
	headerBody := hdr.Bytes()

	// The lead, up to (but excluding) the header checksum.
	leadNoDigest := []byte(Magic)
	leadNoDigest = AppendCompressedInt(leadNoDigest, uint64(overallType))
	leadNoDigest = AppendCompressedInt(leadNoDigest, uint64(len(headerBody)))

	// Header checksum over lead-without-digest followed by the header body.
	toHash := make([]byte, 0, len(leadNoDigest)+len(headerBody))
	toHash = append(toHash, leadNoDigest...)
	toHash = append(toHash, headerBody...)
	headerChecksum, _ := overallType.Sum(toHash)

	out := make([]byte, 0, len(leadNoDigest)+len(headerChecksum)+len(headerBody)+len(body))
	out = append(out, leadNoDigest...)
	out = append(out, headerChecksum...)
	out = append(out, headerBody...)
	out = append(out, body...)

	n, err := w.Write(out)
	return int64(n), err
}
