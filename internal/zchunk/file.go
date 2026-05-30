package zchunk

import (
	"bytes"
	"io"
)

// buildHeader assembles the lead, preface, index and signatures of a zchunk file
// using magic as the lead ID. It computes the preface's data checksum over body
// (zeros when the uncompressed-source flag suppresses it) and the lead's header
// checksum, which is always taken with the embedded Magic substituted for magic
// — so a detached header (DetachedMagic) and its embedded form share a checksum,
// matching the reference. The caller's pre is not mutated. It returns the
// serialised header bytes (without the body).
func buildHeader(magic string, overallType ChecksumType, pre *Preface, idx *Index,
	sigs *Signatures, body []byte) ([]byte, error) {
	digestSize, err := overallType.Size()
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if _, err := idx.WriteTo(&hdr, p.UncompressedSource()); err != nil {
		return nil, err
	}
	if _, err := sigs.WriteTo(&hdr); err != nil {
		return nil, err
	}
	headerBody := hdr.Bytes()

	// Header checksum over lead-without-digest (always with the embedded Magic)
	// followed by the header body.
	hashLead := AppendCompressedInt([]byte(Magic), uint64(overallType))
	hashLead = AppendCompressedInt(hashLead, uint64(len(headerBody)))
	toHash := make([]byte, 0, len(hashLead)+len(headerBody))
	toHash = append(toHash, hashLead...)
	toHash = append(toHash, headerBody...)
	headerChecksum, _ := overallType.Sum(toHash)

	// The written lead carries the requested magic (Magic or DetachedMagic); the
	// rest of the lead is identical to what was hashed.
	out := make([]byte, 0, len(magic)+2*MaxCompressedIntLen+len(headerChecksum)+len(headerBody))
	out = append(out, magic...)
	out = AppendCompressedInt(out, uint64(overallType))
	out = AppendCompressedInt(out, uint64(len(headerBody)))
	out = append(out, headerChecksum...)
	out = append(out, headerBody...)
	return out, nil
}

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
	header, err := buildHeader(Magic, overallType, pre, idx, sigs, body)
	if err != nil {
		return 0, err
	}
	out := make([]byte, 0, len(header)+len(body))
	out = append(out, header...)
	out = append(out, body...)
	n, err := w.Write(out)
	return int64(n), err
}

// WriteDetachedHeader writes a detached zchunk header to w: the lead (with the
// DetachedMagic "\0ZHR1" ID), preface, index and signatures, with no body. It
// takes body only to compute the preface's data checksum, which still describes
// the original file's body; the body itself is not written. The header checksum
// is computed exactly as for an embedded header (with Magic substituted), so the
// reference's reader accepts it. Arguments otherwise match WriteFile.
func WriteDetachedHeader(w io.Writer, overallType ChecksumType, pre *Preface, idx *Index,
	sigs *Signatures, body []byte) (int64, error) {
	header, err := buildHeader(DetachedMagic, overallType, pre, idx, sigs, body)
	if err != nil {
		return 0, err
	}
	n, err := w.Write(header)
	return int64(n), err
}
