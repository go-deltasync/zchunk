package zchunk

import (
	"bytes"
	"fmt"
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

	header := append([]byte(nil), prefix[:leadLen]...)
	header = append(header, hdrBody...)

	return &RemoteHeader{
		Lead:        lead,
		Preface:     pre,
		Index:       idx,
		Signatures:  sigs,
		HeaderBytes: header,
		BodyOffset:  int64(leadLen) + int64(lead.HeaderSize),
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
func DownloadDelta(remote RangeReader, localIndex *Index, localBody io.ReaderAt, out io.Writer) (int64, error) {
	rh, err := ReadRemoteHeader(remote)
	if err != nil {
		return 0, err
	}
	plan, err := PlanDelta(localIndex, rh.Index)
	if err != nil {
		return 0, err
	}

	hn, err := out.Write(rh.HeaderBytes)
	written := int64(hn)
	if err != nil {
		return written, fmt.Errorf("zchunk: write header: %w", err)
	}

	body := offsetRange{rr: remote, base: rh.BodyOffset}
	bn, err := plan.AssembleBody(rh.Index, localBody, body, out)
	written += bn
	return written, err
}
