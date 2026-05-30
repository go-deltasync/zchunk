package zchunk

import (
	"bytes"
	"io"
	"testing"
)

func TestWriteFileRoundTrip(t *testing.T) {
	dict := bytes.Repeat([]byte("DICTIONARY-"), 24)
	data := [][]byte{
		append(append([]byte(nil), dict...), []byte(" tail one")...),
		[]byte("an entirely separate second chunk payload"),
		{}, // empty chunk
	}
	cases := []struct {
		name         string
		overall      ChecksumType
		chunk        ChecksumType
		ct           CompressionType
		uncompressed bool
		useDict      []byte
		nilSigs      bool
	}{
		{"sha256-zstd-dict", SHA256, SHA256, CompressionZstd, false, dict, false},
		{"sha1-none-emptydict-nilsigs", SHA1, SHA512, CompressionNone, false, nil, true},
		{"sha256-zstd-uncompressed", SHA256, SHA256, CompressionZstd, true, dict, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, body := buildBody(t, c.chunk, c.ct, c.uncompressed, c.useDict, data)
			pre := &Preface{CompressionType: c.ct}
			if c.uncompressed {
				pre.Flags = FlagUncompressedSource
			}
			var sigs *Signatures
			if !c.nilSigs {
				sigs = &Signatures{}
			}

			var buf bytes.Buffer
			n, err := WriteFile(&buf, c.overall, pre, idx, sigs, body)
			if err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			all := buf.Bytes()
			if int(n) != len(all) {
				t.Fatalf("WriteFile n=%d, buffer=%d", n, len(all))
			}
			// WriteFile must not mutate the caller's preface.
			if pre.DataChecksum != nil {
				t.Fatal("WriteFile mutated the caller's preface")
			}

			digestSize, _ := c.overall.Size()

			// Read back the full header and validate the layout.
			r := bytes.NewReader(all)
			lead, err := ReadLead(r)
			if err != nil {
				t.Fatalf("ReadLead: %v", err)
			}
			if lead.ChecksumType != c.overall {
				t.Fatalf("lead checksum type = %d, want %d", lead.ChecksumType, c.overall)
			}
			var lb bytes.Buffer
			if _, err := lead.WriteTo(&lb); err != nil {
				t.Fatalf("lead.WriteTo: %v", err)
			}
			leadSize := lb.Len()

			// Header checksum covers lead-without-digest + header body.
			leadNoDigest := all[:leadSize-digestSize]
			storedSum := all[leadSize-digestSize : leadSize]
			headerBody := all[leadSize : leadSize+int(lead.HeaderSize)]
			wantSum, _ := c.overall.Sum(append(append([]byte(nil), leadNoDigest...), headerBody...))
			if !bytes.Equal(storedSum, wantSum) {
				t.Fatal("header checksum mismatch")
			}

			pre2, err := ReadPreface(r, c.overall)
			if err != nil {
				t.Fatalf("ReadPreface: %v", err)
			}
			if c.uncompressed {
				if !bytes.Equal(pre2.DataChecksum, make([]byte, digestSize)) {
					t.Fatal("uncompressed-source data checksum is not zero")
				}
			} else {
				wantDC, _ := c.overall.Sum(body)
				if !bytes.Equal(pre2.DataChecksum, wantDC) {
					t.Fatal("data checksum mismatch")
				}
			}

			idx2, err := ReadIndex(r, pre2.UncompressedSource())
			if err != nil {
				t.Fatalf("ReadIndex: %v", err)
			}
			if _, err := ReadSignatures(r); err != nil {
				t.Fatalf("ReadSignatures: %v", err)
			}

			var out bytes.Buffer
			if _, err := idx2.Extract(r, pre2.CompressionType, &out); err != nil {
				t.Fatalf("Extract: %v", err)
			}
			var want bytes.Buffer
			for _, d := range data {
				want.Write(d)
			}
			if !bytes.Equal(out.Bytes(), want.Bytes()) {
				t.Fatal("round-tripped content mismatch")
			}
		})
	}
}

func TestWriteFileErrors(t *testing.T) {
	idx, body := buildBody(t, SHA256, CompressionZstd, false, nil, [][]byte{[]byte("x")})
	goodPre := &Preface{CompressionType: CompressionZstd}

	// Unknown overall checksum type.
	if _, err := WriteFile(io.Discard, 99, goodPre, idx, nil, body); err == nil {
		t.Fatal("WriteFile accepted unknown overall checksum type")
	}

	// Preface serialisation fails (unknown flags).
	badPre := &Preface{Flags: 1 << 6, CompressionType: CompressionZstd}
	if _, err := WriteFile(io.Discard, SHA256, badPre, idx, nil, body); err == nil {
		t.Fatal("WriteFile accepted an invalid preface")
	}

	// Index serialisation fails (unknown chunk checksum type).
	badIdx := &Index{ChunkChecksumType: 99, Chunks: []IndexEntry{{Digest: nil, CompLength: 0, Length: 0}}}
	if _, err := WriteFile(io.Discard, SHA256, goodPre, badIdx, nil, body); err == nil {
		t.Fatal("WriteFile accepted an invalid index")
	}

	// Signature serialisation fails (non-zero count).
	if _, err := WriteFile(io.Discard, SHA256, goodPre, idx, &Signatures{Count: 1}, body); err == nil {
		t.Fatal("WriteFile accepted unsupported signatures")
	}

	// Underlying writer fails.
	if _, err := WriteFile(&failWriter{failAt: 1}, SHA256, goodPre, idx, nil, body); err == nil {
		t.Fatal("WriteFile ignored a write error")
	}
}

func TestWriteDetachedHeaderRoundTrip(t *testing.T) {
	dict := bytes.Repeat([]byte("DICT-"), 8)
	data := [][]byte{
		append(append([]byte(nil), dict...), []byte(" tail")...),
		[]byte("second chunk payload here"),
	}
	idx, body := buildBody(t, SHA256, CompressionZstd, false, dict, data)
	pre := &Preface{CompressionType: CompressionZstd}

	var buf bytes.Buffer
	n, err := WriteDetachedHeader(&buf, SHA256, pre, idx, nil, body)
	if err != nil {
		t.Fatalf("WriteDetachedHeader: %v", err)
	}
	detached := buf.Bytes()
	if int(n) != len(detached) {
		t.Fatalf("n=%d, buffer=%d", n, len(detached))
	}

	// The detached file carries the "\0ZHR1" magic and no body.
	rh, err := ReadDetachedHeader(bytes.NewReader(detached))
	if err != nil {
		t.Fatalf("ReadDetachedHeader: %v", err)
	}
	if !rh.Lead.Detached {
		t.Fatal("expected a detached lead")
	}
	if int(rh.BodyOffset) != len(detached) {
		t.Fatalf("BodyOffset=%d, want %d (no body in a detached header)", rh.BodyOffset, len(detached))
	}
	// The data checksum still describes the original body.
	wantDC, _ := SHA256.Sum(body)
	if !bytes.Equal(rh.Preface.DataChecksum, wantDC) {
		t.Fatal("detached header data checksum does not describe the body")
	}
	// The same header, embedded, must share the byte layout after the magic.
	var full bytes.Buffer
	if _, err := WriteFile(&full, SHA256, pre, idx, nil, body); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	embedded := full.Bytes()
	if !bytes.Equal(detached[len(DetachedMagic):], embedded[len(Magic):len(detached)]) {
		t.Fatal("detached and embedded headers differ beyond the magic")
	}
}

func TestWriteDetachedHeaderErrors(t *testing.T) {
	idx, body := buildBody(t, SHA256, CompressionZstd, false, nil, [][]byte{[]byte("x")})
	goodPre := &Preface{CompressionType: CompressionZstd}

	// Unknown overall checksum type (propagated from buildHeader).
	if _, err := WriteDetachedHeader(io.Discard, 99, goodPre, idx, nil, body); err == nil {
		t.Fatal("WriteDetachedHeader accepted unknown overall checksum type")
	}
	// Underlying writer fails.
	if _, err := WriteDetachedHeader(&failWriter{failAt: 1}, SHA256, goodPre, idx, nil, body); err == nil {
		t.Fatal("WriteDetachedHeader ignored a write error")
	}
}
