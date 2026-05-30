package zchunk

import (
	"bytes"
	"fmt"
	"hash"
	"io"
)

// maxLeadPrefix is an upper bound on a lead's byte length: the magic, the two
// compressed integers (checksum type and header size) at their widest, and the
// largest possible header digest (SHA-512, 64 bytes). Fetching this many bytes
// is guaranteed to cover the lead of any well-formed file.
const maxLeadPrefix = len(Magic) + 2*MaxCompressedIntLen + 64

// RemoteHeader is a parsed remote zchunk header together with everything a
// client needs to download the file by delta: the verbatim header bytes (so the
// reconstructed file reproduces the remote header exactly) and the absolute
// file offset at which the body begins.
type RemoteHeader struct {
	Lead       *Lead
	Preface    *Preface
	Index      *Index
	Signatures *Signatures
	// HeaderBytes is the verbatim lead+preface+index+signatures.
	HeaderBytes []byte
	// BodyOffset is the absolute file offset at which the body (chunk 0) begins.
	BodyOffset int64
}

// ReadRemoteHeader fetches and parses a remote file's header using absolute
// byte-range reads. remote.ReadRange offsets are absolute file offsets. It
// issues two range requests: one for the lead and one for the header proper.
func ReadRemoteHeader(remote RangeReader) (*RemoteHeader, error) {
	prefix, err := remote.ReadRange(0, int64(maxLeadPrefix))
	if err != nil {
		return nil, fmt.Errorf("zchunk: fetch lead: %w", err)
	}
	lead, err := ReadLead(bytes.NewReader(prefix))
	if err != nil {
		return nil, err
	}
	// ReadLead validated the checksum type, so Size cannot fail here.
	size, _ := lead.ChecksumType.Size()
	leadLen := len(Magic) +
		len(AppendCompressedInt(nil, uint64(lead.ChecksumType))) +
		len(AppendCompressedInt(nil, lead.HeaderSize)) +
		size

	hdrBody, err := remote.ReadRange(int64(leadLen), int64(lead.HeaderSize))
	if err != nil {
		return nil, fmt.Errorf("zchunk: fetch header: %w", err)
	}
	if int64(len(hdrBody)) != int64(lead.HeaderSize) {
		return nil, fmt.Errorf("zchunk: fetch header: got %d bytes, want %d",
			len(hdrBody), lead.HeaderSize)
	}
	return parseHeader(lead, prefix[:leadLen], hdrBody)
}

// ReadDetachedHeader reads a detached zchunk header (lead ID "\0ZHR1", followed
// by preface/index/signatures and no body) from r, consuming exactly the
// header's bytes. It verifies the header against its embedded checksum and
// returns the parsed header; BodyOffset is the offset at which the body begins
// in the corresponding full file, so the result can drive a delta download of
// that file. An embedded-magic header is accepted too (its trailing body, if
// any, is left unread).
func ReadDetachedHeader(r io.Reader) (*RemoteHeader, error) {
	// Capture the lead's raw bytes (preserving its actual magic) while parsing.
	var leadBuf bytes.Buffer
	lead, err := ReadLead(io.TeeReader(r, &leadBuf))
	if err != nil {
		return nil, err
	}
	hdrBody := make([]byte, lead.HeaderSize)
	if _, err := io.ReadFull(r, hdrBody); err != nil {
		return nil, fmt.Errorf("zchunk: read detached header: %w", err)
	}
	return parseHeader(lead, leadBuf.Bytes(), hdrBody)
}

// parseHeader verifies hdrBody against lead's embedded header checksum, parses
// the preface/index/signatures from it, and assembles a RemoteHeader. leadBytes
// is the verbatim lead (used to reproduce HeaderBytes and locate the body).
func parseHeader(lead *Lead, leadBytes, hdrBody []byte) (*RemoteHeader, error) {
	// Validate the header against its embedded checksum before trusting any of
	// the index offsets/digests a caller will plan a download around.
	if err := lead.VerifyHeader(hdrBody); err != nil {
		return nil, err
	}

	hr := bytes.NewReader(hdrBody)
	pre, err := ReadPreface(hr, lead.ChecksumType)
	if err != nil {
		return nil, err
	}
	idx, err := ReadIndex(hr, pre.UncompressedSource())
	if err != nil {
		return nil, err
	}
	sigs, err := ReadSignatures(hr)
	if err != nil {
		return nil, err
	}

	header := make([]byte, 0, len(leadBytes)+len(hdrBody))
	header = append(header, leadBytes...)
	header = append(header, hdrBody...)

	return &RemoteHeader{
		Lead:        lead,
		Preface:     pre,
		Index:       idx,
		Signatures:  sigs,
		HeaderBytes: header,
		BodyOffset:  int64(len(leadBytes)) + int64(len(hdrBody)),
	}, nil
}

// offsetRange shifts an absolute-range reader into a body-relative one by adding
// a fixed base to every requested offset.
type offsetRange struct {
	rr   RangeReader
	base int64
}

func (o offsetRange) ReadRange(offset, length int64) ([]byte, error) {
	return o.rr.ReadRange(o.base+offset, length)
}

// DownloadDelta reconstructs a remote zchunk file into out, fetching only the
// body chunks not already present locally. remote performs absolute byte-range
// reads of the remote file (e.g. an HTTPRangeReader with Base 0); localIndex
// and localBody describe a local copy whose matching chunks are reused. The
// output is the remote header verbatim followed by the assembled body, so it
// round-trips through the readers and Extract. It returns the number of bytes
// written.
//
// As a final integrity check the assembled body is hashed (with the lead's
// checksum type) and matched against the preface's whole-file data checksum,
// catching any inconsistency the per-chunk digests would miss. The data
// checksum is not generated for an uncompressed-source file, so verification is
// skipped there, matching the reference.
func DownloadDelta(remote RangeReader, localIndex *Index, localBody io.ReaderAt, out io.Writer) (int64, error) {
	rh, err := ReadRemoteHeader(remote)
	if err != nil {
		return 0, err
	}
	return DownloadDeltaWithHeader(rh, remote, localIndex, localBody, out)
}

// DownloadDeltaWithHeader reconstructs a full zchunk file into out from an
// already-parsed header rh and a body source, fetching only the chunks missing
// locally. It is the engine behind DownloadDelta, but accepts the header
// separately so a client that fetched a standalone (detached) header — via
// ReadDetachedHeader — can drive the download from it: when rh describes a
// detached header (lead ID "\0ZHR1"), the embedded magic ("\0ZCK1") is restored
// so the reconstructed file is a normal full file rather than a detached one.
//
// remoteBody serves absolute byte-range reads of the file's body region;
// rh.BodyOffset locates where the body begins, so remoteBody is the same
// absolute-offset reader used to fetch the header. localIndex/localBody describe
// the local copy whose matching chunks are reused. The data checksum is verified
// exactly as in DownloadDelta. It returns the number of bytes written.
func DownloadDeltaWithHeader(rh *RemoteHeader, remoteBody RangeReader, localIndex *Index, localBody io.ReaderAt, out io.Writer) (int64, error) {
	plan, err := PlanDelta(localIndex, rh.Index)
	if err != nil {
		return 0, err
	}

	// Reproduce a full-file header: a detached header is byte-identical to an
	// embedded one except for its 5-byte magic, so swap "\0ZHR1" back to
	// "\0ZCK1" (its checksum is computed with the embedded magic regardless).
	headerBytes := rh.HeaderBytes
	if rh.Lead.Detached {
		headerBytes = append([]byte(Magic), rh.HeaderBytes[len(DetachedMagic):]...)
	}

	hn, err := out.Write(headerBytes)
	written := int64(hn)
	if err != nil {
		return written, fmt.Errorf("zchunk: write header: %w", err)
	}

	// Tee the body through a hasher so it can be checked against the preface's
	// data checksum without buffering the whole body.
	bodyOut := out
	var h hash.Hash
	if !rh.Preface.UncompressedSource() {
		// ReadLead validated the checksum type, so newHash cannot fail here.
		h, _ = rh.Lead.ChecksumType.newHash()
		bodyOut = io.MultiWriter(out, h)
	}

	body := offsetRange{rr: remoteBody, base: rh.BodyOffset}
	bn, err := plan.AssembleBody(rh.Index, localBody, body, bodyOut)
	written += bn
	if err != nil {
		return written, err
	}
	if h != nil {
		size, _ := rh.Lead.ChecksumType.Size()
		if sum := h.Sum(nil)[:size]; !bytes.Equal(sum, rh.Preface.DataChecksum) {
			return written, fmt.Errorf("zchunk: data checksum mismatch")
		}
	}
	return written, nil
}
