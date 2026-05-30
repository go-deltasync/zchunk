package zchunk

import (
	"bytes"
	"io"
	"testing"
)

// byteRange serves absolute byte ranges from an in-memory file, clamping the
// length to what is available (as an HTTP range server does near EOF).
type byteRange struct {
	data []byte
	err  error
}

func (b byteRange) ReadRange(offset, length int64) ([]byte, error) {
	if b.err != nil {
		return nil, b.err
	}
	if offset > int64(len(b.data)) {
		offset = int64(len(b.data))
	}
	end := offset + length
	if end > int64(len(b.data)) {
		end = int64(len(b.data))
	}
	return append([]byte(nil), b.data[offset:end]...), nil
}

// failSecondRange serves the lead from data but errors on the header fetch.
type failSecondRange struct {
	data  []byte
	calls int
}

func (f *failSecondRange) ReadRange(offset, length int64) ([]byte, error) {
	f.calls++
	if f.calls >= 2 {
		return nil, errBoom
	}
	end := offset + length
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}
	return append([]byte(nil), f.data[offset:end]...), nil
}

// buildFullFile writes a complete CompressionNone file (lead+header+body) whose
// index is idx and body is body.
func buildFullFile(t *testing.T, idx *Index, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	pre := &Preface{CompressionType: CompressionNone}
	if _, err := WriteFile(&buf, SHA256, pre, idx, nil, body); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return buf.Bytes()
}

// craftFile prepends a lead with a correct header checksum (so the file passes
// ReadRemoteHeader's header verification and the test exercises a later parse
// step) to a hand-built header body.
func craftFile(t *testing.T, ckType ChecksumType, headerBody []byte) []byte {
	t.Helper()
	lead := &Lead{ChecksumType: ckType, HeaderSize: uint64(len(headerBody))}
	// Compute the header checksum the same way WriteFile/VerifyHeader do.
	leadNoDigest := AppendCompressedInt(AppendCompressedInt([]byte(Magic), uint64(ckType)), lead.HeaderSize)
	sum, err := ckType.Sum(append(append([]byte(nil), leadNoDigest...), headerBody...))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	lead.HeaderChecksum = sum
	var buf bytes.Buffer
	if _, err := lead.WriteTo(&buf); err != nil {
		t.Fatalf("lead WriteTo: %v", err)
	}
	buf.Write(headerBody)
	return buf.Bytes()
}

func TestDownloadDelta(t *testing.T) {
	target, local, targetBody, localBody := mixedSetup(t)
	file := buildFullFile(t, target, targetBody)

	var out bytes.Buffer
	n, err := DownloadDelta(byteRange{data: file}, local, bytes.NewReader(localBody), &out)
	if err != nil {
		t.Fatalf("DownloadDelta: %v", err)
	}
	if int(n) != len(file) || !bytes.Equal(out.Bytes(), file) {
		t.Fatalf("download mismatch: n=%d, want %d, equal=%v", n, len(file), bytes.Equal(out.Bytes(), file))
	}
}

func TestReadRemoteHeaderErrors(t *testing.T) {
	target, _, targetBody, _ := mixedSetup(t)
	file := buildFullFile(t, target, targetBody)

	t.Run("fetch-lead-error", func(t *testing.T) {
		if _, err := ReadRemoteHeader(byteRange{err: errBoom}); err == nil {
			t.Fatal("expected fetch-lead error")
		}
	})

	t.Run("bad-lead-magic", func(t *testing.T) {
		if _, err := ReadRemoteHeader(byteRange{data: bytes.Repeat([]byte("X"), 64)}); err == nil {
			t.Fatal("expected bad-magic error")
		}
	})

	t.Run("fetch-header-error", func(t *testing.T) {
		if _, err := ReadRemoteHeader(&failSecondRange{data: file}); err == nil {
			t.Fatal("expected fetch-header error")
		}
	})

	t.Run("short-header", func(t *testing.T) {
		rh, err := ReadRemoteHeader(byteRange{data: file})
		if err != nil {
			t.Fatalf("ReadRemoteHeader: %v", err)
		}
		truncated := file[:rh.BodyOffset-1]
		if _, err := ReadRemoteHeader(byteRange{data: truncated}); err == nil {
			t.Fatal("expected short-header error")
		}
	})

	t.Run("header-checksum-mismatch", func(t *testing.T) {
		// Corrupt one header byte after the lead: the embedded checksum no
		// longer matches, so verification must fail before any parsing.
		corrupt := append([]byte(nil), file...)
		rh, err := ReadRemoteHeader(byteRange{data: file})
		if err != nil {
			t.Fatalf("ReadRemoteHeader: %v", err)
		}
		corrupt[rh.BodyOffset-1] ^= 0xff // last header byte
		if _, err := ReadRemoteHeader(byteRange{data: corrupt}); err == nil {
			t.Fatal("expected header checksum mismatch")
		}
	})

	t.Run("bad-preface", func(t *testing.T) {
		// One header byte: ReadPreface cannot even read the data checksum.
		bad := craftFile(t, SHA256, []byte{0x80})
		if _, err := ReadRemoteHeader(byteRange{data: bad}); err == nil {
			t.Fatal("expected preface error")
		}
	})

	t.Run("bad-index", func(t *testing.T) {
		var hb bytes.Buffer
		pre := &Preface{CompressionType: CompressionNone, DataChecksum: make([]byte, 32)}
		if _, err := pre.WriteTo(&hb); err != nil {
			t.Fatalf("preface WriteTo: %v", err)
		}
		body := hb.Bytes()
		body = AppendCompressedInt(body, 200) // index size claims 200 bytes...
		body = append(body, 0x00)             // ...but only 1 byte follows
		bad := craftFile(t, SHA256, body)
		if _, err := ReadRemoteHeader(byteRange{data: bad}); err == nil {
			t.Fatal("expected index error")
		}
	})

	t.Run("bad-signatures", func(t *testing.T) {
		var hb bytes.Buffer
		pre := &Preface{CompressionType: CompressionNone, DataChecksum: make([]byte, 32)}
		if _, err := pre.WriteTo(&hb); err != nil {
			t.Fatalf("preface WriteTo: %v", err)
		}
		idx := &Index{ChunkChecksumType: SHA256, Chunks: []IndexEntry{
			{Digest: make([]byte, 32), CompLength: 0, Length: 0},
		}}
		if _, err := idx.WriteTo(&hb, false); err != nil {
			t.Fatalf("index WriteTo: %v", err)
		}
		body := AppendCompressedInt(hb.Bytes(), 5) // non-zero signature count is rejected
		bad := craftFile(t, SHA256, body)
		if _, err := ReadRemoteHeader(byteRange{data: bad}); err == nil {
			t.Fatal("expected signatures error")
		}
	})
}

func TestReadDetachedHeaderErrors(t *testing.T) {
	target, _, targetBody, _ := mixedSetup(t)
	pre := &Preface{CompressionType: CompressionNone}
	var buf bytes.Buffer
	if _, err := WriteDetachedHeader(&buf, SHA256, pre, target, nil, targetBody); err != nil {
		t.Fatalf("WriteDetachedHeader: %v", err)
	}
	detached := buf.Bytes()

	t.Run("bad-lead", func(t *testing.T) {
		if _, err := ReadDetachedHeader(bytes.NewReader([]byte("nope"))); err == nil {
			t.Fatal("expected lead error")
		}
	})

	t.Run("truncated-header-body", func(t *testing.T) {
		// A valid lead but the header body is cut short.
		if _, err := ReadDetachedHeader(bytes.NewReader(detached[:len(detached)-1])); err == nil {
			t.Fatal("expected truncated header error")
		}
	})

	t.Run("checksum-mismatch", func(t *testing.T) {
		corrupt := append([]byte(nil), detached...)
		corrupt[len(corrupt)-1] ^= 0xff // flip a header-body byte
		if _, err := ReadDetachedHeader(bytes.NewReader(corrupt)); err == nil {
			t.Fatal("expected header checksum mismatch")
		}
	})
}

func TestDownloadDeltaErrors(t *testing.T) {
	target, local, targetBody, localBody := mixedSetup(t)
	file := buildFullFile(t, target, targetBody)

	t.Run("read-header-error", func(t *testing.T) {
		if _, err := DownloadDelta(byteRange{err: errBoom}, local, bytes.NewReader(localBody), io.Discard); err == nil {
			t.Fatal("expected read-header error")
		}
	})

	t.Run("plan-error", func(t *testing.T) {
		badLocal := &Index{ChunkChecksumType: SHA1, Chunks: []IndexEntry{
			{Digest: make([]byte, 20), CompLength: 1, Length: 1},
		}}
		if _, err := DownloadDelta(byteRange{data: file}, badLocal, bytes.NewReader(localBody), io.Discard); err == nil {
			t.Fatal("expected plan (checksum-type mismatch) error")
		}
	})

	t.Run("write-header-error", func(t *testing.T) {
		if _, err := DownloadDelta(byteRange{data: file}, local, bytes.NewReader(localBody), &failWriter{failAt: 1}); err == nil {
			t.Fatal("expected write-header error")
		}
	})

	t.Run("assemble-body-error", func(t *testing.T) {
		// Header write (call 1) succeeds; the first body chunk write (call 2) fails.
		if _, err := DownloadDelta(byteRange{data: file}, local, bytes.NewReader(localBody), &failWriter{failAt: 2}); err == nil {
			t.Fatal("expected assemble-body error")
		}
	})
}

// craftFileBody builds a complete file from a hand-written preface/index (so the
// preface's DataChecksum can be set freely) with a valid header checksum,
// followed by body.
func craftFileBody(t *testing.T, pre *Preface, idx *Index, body []byte) []byte {
	t.Helper()
	var hb bytes.Buffer
	if _, err := pre.WriteTo(&hb); err != nil {
		t.Fatalf("preface WriteTo: %v", err)
	}
	if _, err := idx.WriteTo(&hb, pre.UncompressedSource()); err != nil {
		t.Fatalf("index WriteTo: %v", err)
	}
	if _, err := (&Signatures{}).WriteTo(&hb); err != nil {
		t.Fatalf("signatures WriteTo: %v", err)
	}
	return append(craftFile(t, SHA256, hb.Bytes()), body...)
}

func TestDownloadDeltaDataChecksum(t *testing.T) {
	target, _, targetBody, _ := mixedSetup(t)
	emptyLocal := &Index{ChunkChecksumType: SHA256} // nothing reusable: fetch all

	t.Run("mismatch", func(t *testing.T) {
		// Valid header (checksum matches) and valid per-chunk digests, but the
		// preface's whole-file data checksum is wrong for the body.
		pre := &Preface{CompressionType: CompressionNone, DataChecksum: bytes.Repeat([]byte{0xff}, 32)}
		file := craftFileBody(t, pre, target, targetBody)
		if _, err := DownloadDelta(byteRange{data: file}, emptyLocal, bytes.NewReader(nil), io.Discard); err == nil {
			t.Fatal("expected data checksum mismatch")
		}
	})

	t.Run("uncompressed-source-skips", func(t *testing.T) {
		// An uncompressed-source file suppresses the data checksum (zeros), so
		// verification must be skipped rather than fail.
		uidx := &Index{ChunkChecksumType: SHA256}
		for _, c := range target.Chunks {
			c.UncompressedDigest = make([]byte, 32)
			uidx.Chunks = append(uidx.Chunks, c)
		}
		pre := &Preface{CompressionType: CompressionNone, Flags: FlagUncompressedSource}
		var buf bytes.Buffer
		if _, err := WriteFile(&buf, SHA256, pre, uidx, nil, targetBody); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		file := buf.Bytes()
		var out bytes.Buffer
		n, err := DownloadDelta(byteRange{data: file}, emptyLocal, bytes.NewReader(nil), &out)
		if err != nil {
			t.Fatalf("DownloadDelta: %v", err)
		}
		if int(n) != len(file) || !bytes.Equal(out.Bytes(), file) {
			t.Fatalf("download mismatch: n=%d, want %d", n, len(file))
		}
	})
}
