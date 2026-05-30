package zchunk

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestSignaturesRoundTrip(t *testing.T) {
	s := &Signatures{Count: 0}
	var buf bytes.Buffer
	n, err := s.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if int(n) != buf.Len() {
		t.Fatalf("WriteTo n=%d, buffer=%d", n, buf.Len())
	}
	got, err := ReadSignatures(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadSignatures: %v", err)
	}
	if got.Count != 0 {
		t.Fatalf("round-trip count = %d, want 0", got.Count)
	}
}

func TestReadSignaturesErrors(t *testing.T) {
	// Truncated count.
	if _, err := ReadSignatures(bytes.NewReader(nil)); err == nil {
		t.Fatal("expected error on truncated signature count")
	}
	// Non-zero count: unsupported.
	if _, err := ReadSignatures(bytes.NewReader(AppendCompressedInt(nil, 1))); err == nil {
		t.Fatal("expected error on non-zero signature count")
	}
}

func TestSignaturesWriteToErrors(t *testing.T) {
	// Non-zero count: unsupported.
	if _, err := (&Signatures{Count: 2}).WriteTo(io.Discard); err == nil {
		t.Fatal("WriteTo accepted a non-zero signature count")
	}
	// Underlying writer fails.
	if _, err := (&Signatures{Count: 0}).WriteTo(&failWriter{failAt: 1}); !errors.Is(err, errBoom) {
		t.Fatalf("WriteTo write error = %v, want errBoom", err)
	}
}
