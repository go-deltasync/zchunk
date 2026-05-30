package zchunk

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

var errBoom = errors.New("boom")

// failWriter fails on its failAt-th Write call (1-based).
type failWriter struct {
	failAt int
	calls  int
}

func (w *failWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAt {
		return 0, errBoom
	}
	return len(p), nil
}

// errByteReader yields data bytes until failAt, then a non-EOF error.
type errByteReader struct {
	data   []byte
	pos    int
	failAt int
}

func (r *errByteReader) ReadByte() (byte, error) {
	if r.pos >= r.failAt {
		return 0, errBoom
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func TestMagics(t *testing.T) {
	if Magic != "\x00ZCK1" {
		t.Fatalf("Magic = %q, want \\x00ZCK1", Magic)
	}
	if DetachedMagic != "\x00ZHR1" {
		t.Fatalf("DetachedMagic = %q, want \\x00ZHR1", DetachedMagic)
	}
}

func TestCompressedIntRoundTrip(t *testing.T) {
	values := []uint64{
		0, 1, 0x7f, 0x80, 0x81, 0x3fff, 0x4000,
		1 << 20, 1 << 35, 1<<63 | 1, ^uint64(0),
	}
	for _, v := range values {
		enc := AppendCompressedInt(nil, v)
		if len(enc) == 0 || len(enc) > MaxCompressedIntLen {
			t.Fatalf("v=%#x: encoded length %d out of range", v, len(enc))
		}
		got, err := ReadCompressedInt(bytes.NewReader(enc))
		if err != nil {
			t.Fatalf("v=%#x: ReadCompressedInt: %v", v, err)
		}
		if got != v {
			t.Fatalf("round-trip v=%#x got %#x", v, got)
		}
	}
}

func TestCompressedIntEncodingShape(t *testing.T) {
	// Per zchunk: non-final bytes have the high bit CLEAR, the final byte SET.
	// 0 fits in one (final) byte: 0x00 | 0x80.
	if enc := AppendCompressedInt(nil, 0); !bytes.Equal(enc, []byte{0x80}) {
		t.Fatalf("0 -> %v, want [0x80]", enc)
	}
	// 0x7f is the largest single-byte value: 0x7f | 0x80 = 0xff.
	if enc := AppendCompressedInt(nil, 0x7f); !bytes.Equal(enc, []byte{0xff}) {
		t.Fatalf("0x7f -> %v, want [0xff]", enc)
	}
	// 0x80 spills: low group 0 (non-final), then 1 (final).
	if enc := AppendCompressedInt(nil, 0x80); !bytes.Equal(enc, []byte{0x00, 0x81}) {
		t.Fatalf("0x80 -> %v, want [0x00 0x81]", enc)
	}
	// Max uint64: nine non-final 0x7f groups then the top bit as 0x81.
	enc := AppendCompressedInt(nil, ^uint64(0))
	if len(enc) != MaxCompressedIntLen {
		t.Fatalf("max uint64 -> %d bytes, want %d", len(enc), MaxCompressedIntLen)
	}
	if enc[MaxCompressedIntLen-1] != 0x81 {
		t.Fatalf("max uint64 final byte = %#x, want 0x81", enc[MaxCompressedIntLen-1])
	}
}

func TestReadCompressedIntTruncatedEmpty(t *testing.T) {
	if _, err := ReadCompressedInt(bytes.NewReader(nil)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("empty input: err = %v, want ErrTruncated", err)
	}
}

func TestReadCompressedIntTruncatedMidInt(t *testing.T) {
	// A non-final byte (high bit clear) then EOF.
	if _, err := ReadCompressedInt(bytes.NewReader([]byte{0x00})); !errors.Is(err, ErrTruncated) {
		t.Fatalf("mid-int input: err = %v, want ErrTruncated", err)
	}
}

func TestReadCompressedIntOverflowFinalGroup(t *testing.T) {
	// Nine non-final groups then a final byte whose payload exceeds bit 63.
	buf := bytes.Repeat([]byte{0x00}, MaxCompressedIntLen-1)
	buf = append(buf, 0x82) // final (0x80) with payload 0x02 > 0x01
	if _, err := ReadCompressedInt(bytes.NewReader(buf)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("final-group overflow: err = %v, want ErrOverflow", err)
	}
}

func TestReadCompressedIntOverflowContinuation(t *testing.T) {
	// Ten non-final bytes: an 11th group would be required.
	buf := bytes.Repeat([]byte{0x00}, MaxCompressedIntLen)
	if _, err := ReadCompressedInt(bytes.NewReader(buf)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("continuation overflow: err = %v, want ErrOverflow", err)
	}
}

func TestReadCompressedIntReadError(t *testing.T) {
	// First byte non-final (forces a second read), which then fails.
	r := &errByteReader{data: []byte{0x00}, failAt: 1}
	if _, err := ReadCompressedInt(r); !errors.Is(err, errBoom) {
		t.Fatalf("read error: err = %v, want errBoom", err)
	}
}

func TestChecksumTypeSize(t *testing.T) {
	cases := []struct {
		t    ChecksumType
		size int
	}{
		{SHA1, 20}, {SHA256, 32}, {SHA512, 64}, {SHA512128, 16},
	}
	for _, c := range cases {
		got, err := c.t.Size()
		if err != nil {
			t.Fatalf("type %d: %v", uint64(c.t), err)
		}
		if got != c.size {
			t.Fatalf("type %d size = %d, want %d", uint64(c.t), got, c.size)
		}
	}
	if _, err := ChecksumType(99).Size(); err == nil {
		t.Fatal("unknown checksum type accepted")
	}
}

// leadBytes builds a raw lead for the given magic, checksum type and header
// size, with a checksum of the type's digest length filled with fill.
func leadBytes(t *testing.T, magic string, ct ChecksumType, headerSize uint64, fill byte) []byte {
	t.Helper()
	size, err := ct.Size()
	if err != nil {
		t.Fatalf("Size(%d): %v", uint64(ct), err)
	}
	buf := []byte(magic)
	buf = AppendCompressedInt(buf, uint64(ct))
	buf = AppendCompressedInt(buf, headerSize)
	buf = append(buf, bytes.Repeat([]byte{fill}, size)...)
	return buf
}

func TestLeadRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		magic   string
		det     bool
		ct      ChecksumType
		hsize   uint64
	}{
		{"zck1-sha256", Magic, false, SHA256, 4096},
		{"detached-sha1", DetachedMagic, true, SHA1, 0x123456},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := leadBytes(t, tc.magic, tc.ct, tc.hsize, 0xab)
			lead, err := ReadLead(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("ReadLead: %v", err)
			}
			if lead.Detached != tc.det || lead.ChecksumType != tc.ct || lead.HeaderSize != tc.hsize {
				t.Fatalf("parsed %+v, want det=%v ct=%d hsize=%d", lead, tc.det, tc.ct, tc.hsize)
			}
			var out bytes.Buffer
			n, err := lead.WriteTo(&out)
			if err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
			if int(n) != out.Len() || !bytes.Equal(out.Bytes(), raw) {
				t.Fatalf("re-serialised lead differs from input (n=%d)", n)
			}
		})
	}
}

func TestReadLeadErrors(t *testing.T) {
	good := leadBytes(t, Magic, SHA256, 4096, 0xcd)
	cases := []struct {
		name string
		data []byte
	}{
		{"short-id", []byte{0x00, 'Z'}},
		{"bad-magic", []byte("HELLO")},
		{"truncated-checksum-type", []byte(Magic)},                      // EOF reading type
		{"unknown-checksum-type", append([]byte(Magic), AppendCompressedInt(nil, 99)...)},
		{"truncated-header-size", append([]byte(Magic), AppendCompressedInt(nil, uint64(SHA256))...)},
		{"truncated-header-checksum", good[:len(good)-1]},               // one digest byte short
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ReadLead(bytes.NewReader(c.data)); err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestLeadWriteToErrors(t *testing.T) {
	// Unknown checksum type.
	bad := &Lead{ChecksumType: 99, HeaderChecksum: nil}
	if _, err := bad.WriteTo(io.Discard); err == nil {
		t.Fatal("WriteTo accepted unknown checksum type")
	}
	// Checksum length disagrees with the type's digest size.
	mismatch := &Lead{ChecksumType: SHA256, HeaderChecksum: make([]byte, 4)}
	if _, err := mismatch.WriteTo(io.Discard); err == nil {
		t.Fatal("WriteTo accepted mismatched checksum length")
	}
	// Underlying writer fails.
	ok := &Lead{ChecksumType: SHA1, HeaderSize: 1, HeaderChecksum: make([]byte, 20)}
	if _, err := ok.WriteTo(&failWriter{failAt: 1}); !errors.Is(err, errBoom) {
		t.Fatalf("WriteTo write error = %v, want errBoom", err)
	}
}

func TestVerifyHeader(t *testing.T) {
	headerBody := []byte("preface+index+signatures bytes")
	// A correct checksum, computed the same way WriteFile does.
	leadNoDigest := AppendCompressedInt(AppendCompressedInt([]byte(Magic), uint64(SHA256)), uint64(len(headerBody)))
	good, err := SHA256.Sum(append(append([]byte(nil), leadNoDigest...), headerBody...))
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}

	t.Run("ok", func(t *testing.T) {
		lead := &Lead{ChecksumType: SHA256, HeaderSize: uint64(len(headerBody)), HeaderChecksum: good}
		if err := lead.VerifyHeader(headerBody); err != nil {
			t.Fatalf("VerifyHeader: %v", err)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		lead := &Lead{ChecksumType: SHA256, HeaderSize: uint64(len(headerBody)), HeaderChecksum: bytes.Repeat([]byte{0xff}, 32)}
		if err := lead.VerifyHeader(headerBody); err == nil {
			t.Fatal("expected header checksum mismatch")
		}
	})

	t.Run("unknown-checksum-type", func(t *testing.T) {
		lead := &Lead{ChecksumType: 99, HeaderSize: uint64(len(headerBody))}
		if err := lead.VerifyHeader(headerBody); err == nil {
			t.Fatal("expected unknown checksum type error")
		}
	})
}
