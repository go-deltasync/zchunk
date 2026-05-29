package zchunk

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestMagic(t *testing.T) {
	if len(Magic) != 5 {
		t.Fatalf("Magic length = %d, want 5", len(Magic))
	}
	if Magic[0] != 0x00 || Magic[1:] != "ZCK1" {
		t.Fatalf("Magic = %q, want \\x00ZCK1", Magic)
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
	// Values < 0x80 encode to a single byte with no continuation flag.
	if enc := AppendCompressedInt(nil, 0x7f); !bytes.Equal(enc, []byte{0x7f}) {
		t.Fatalf("0x7f -> %v, want [0x7f]", enc)
	}
	// 0x80 spills into a second group: low 7 bits (0) with continuation, then 1.
	if enc := AppendCompressedInt(nil, 0x80); !bytes.Equal(enc, []byte{0x80, 0x01}) {
		t.Fatalf("0x80 -> %v, want [0x80 0x01]", enc)
	}
	// Max uint64 is 9 full groups plus the single top bit.
	enc := AppendCompressedInt(nil, ^uint64(0))
	if len(enc) != MaxCompressedIntLen {
		t.Fatalf("max uint64 encoded to %d bytes, want %d", len(enc), MaxCompressedIntLen)
	}
	if enc[MaxCompressedIntLen-1] != 0x01 {
		t.Fatalf("max uint64 final group = %#x, want 0x01", enc[MaxCompressedIntLen-1])
	}
}

func TestReadCompressedIntTruncatedEmpty(t *testing.T) {
	if _, err := ReadCompressedInt(bytes.NewReader(nil)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("empty input: err = %v, want ErrTruncated", err)
	}
}

func TestReadCompressedIntTruncatedMidInt(t *testing.T) {
	// A single byte with the continuation flag set, then EOF.
	if _, err := ReadCompressedInt(bytes.NewReader([]byte{0x80})); !errors.Is(err, ErrTruncated) {
		t.Fatalf("mid-int input: err = %v, want ErrTruncated", err)
	}
}

func TestReadCompressedIntOverflowFinalGroup(t *testing.T) {
	// Nine continuation bytes then a final group whose payload exceeds bit 63.
	buf := bytes.Repeat([]byte{0x80}, MaxCompressedIntLen-1)
	buf = append(buf, 0x02) // 10th group payload 0x02 > 0x01
	if _, err := ReadCompressedInt(bytes.NewReader(buf)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("final-group overflow: err = %v, want ErrOverflow", err)
	}
}

func TestReadCompressedIntOverflowContinuation(t *testing.T) {
	// Ten continuation bytes: an 11th group would be required.
	buf := bytes.Repeat([]byte{0x80}, MaxCompressedIntLen)
	if _, err := ReadCompressedInt(bytes.NewReader(buf)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("continuation overflow: err = %v, want ErrOverflow", err)
	}
}

// errByteReader yields one byte then a non-EOF error, to exercise the
// pass-through read-error path.
type errByteReader struct {
	data   []byte
	pos    int
	failAt int
}

var errBoom = errors.New("boom")

func (r *errByteReader) ReadByte() (byte, error) {
	if r.pos >= r.failAt {
		return 0, errBoom
	}
	b := r.data[r.pos]
	r.pos++
	return b, nil
}

func TestReadCompressedIntReadError(t *testing.T) {
	r := &errByteReader{data: []byte{0x80, 0x80}, failAt: 1}
	if _, err := ReadCompressedInt(r); !errors.Is(err, errBoom) {
		t.Fatalf("read error: err = %v, want errBoom", err)
	}
}

// Guard that ReadCompressedInt only consumes a ByteReader (no large buffering).
var _ io.ByteReader = (*bytes.Reader)(nil)
