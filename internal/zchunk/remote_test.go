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

// craftFile prepends a valid lead (zero header checksum, which ReadRemoteHeader
// does not verify) to a hand-built header body.
func craftFile(t *testing.T, ckType ChecksumType, headerBody []byte) []byte {
	t.Helper()
	size, _ := ckType.Size()
	lead := &Lead{ChecksumType: ckType, HeaderSize: uint64(len(headerBody)), HeaderChecksum: make([]byte, size)}
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
