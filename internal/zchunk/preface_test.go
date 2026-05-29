package zchunk

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

func cksum(t *testing.T, ct ChecksumType, fill byte) []byte {
	t.Helper()
	size, err := ct.Size()
	if err != nil {
		t.Fatalf("Size(%d): %v", uint64(ct), err)
	}
	return bytes.Repeat([]byte{fill}, size)
}

func TestPrefaceRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		pre  *Preface
		ct   ChecksumType
	}{
		{
			name: "no-flags-zstd",
			ct:   SHA256,
			pre:  &Preface{Flags: 0, CompressionType: CompressionZstd},
		},
		{
			name: "streams-and-uncompressed-none",
			ct:   SHA1,
			pre:  &Preface{Flags: FlagDataStreams | FlagUncompressedSource, CompressionType: CompressionNone},
		},
		{
			name: "optional-elements",
			ct:   SHA256,
			pre: &Preface{
				Flags:           FlagOptionalElements,
				CompressionType: CompressionZstd,
				OptionalElements: []OptionalElement{
					{ID: 5, Data: []byte("abc")},
					{ID: 7, Data: []byte{}},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.pre.DataChecksum = cksum(t, c.ct, 0x5a)
			var buf bytes.Buffer
			n, err := c.pre.WriteTo(&buf)
			if err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
			if int(n) != buf.Len() {
				t.Fatalf("WriteTo n=%d, buffer=%d", n, buf.Len())
			}
			got, err := ReadPreface(bytes.NewReader(buf.Bytes()), c.ct)
			if err != nil {
				t.Fatalf("ReadPreface: %v", err)
			}
			// Normalise empty/nil optional element slices for comparison.
			if len(got.OptionalElements) == 0 {
				got.OptionalElements = c.pre.OptionalElements
			}
			if !reflect.DeepEqual(got, c.pre) {
				t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, c.pre)
			}
		})
	}
}

func TestPrefaceFlagAccessors(t *testing.T) {
	all := &Preface{Flags: FlagDataStreams | FlagOptionalElements | FlagUncompressedSource}
	if !all.HasDataStreams() || !all.HasOptionalElements() || !all.UncompressedSource() {
		t.Fatal("all-flags preface: an accessor returned false")
	}
	none := &Preface{Flags: 0}
	if none.HasDataStreams() || none.HasOptionalElements() || none.UncompressedSource() {
		t.Fatal("no-flags preface: an accessor returned true")
	}
}

func TestReadPrefaceErrors(t *testing.T) {
	sum := cksum(t, SHA256, 0x11)
	ci := func(v uint64) []byte { return AppendCompressedInt(nil, v) }
	concat := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}

	cases := []struct {
		name   string
		ct     ChecksumType
		data   []byte
	}{
		{"unknown-checksum-type", ChecksumType(99), nil},
		{"truncated-data-checksum", SHA256, sum[:10]},
		{"flags-read-error", SHA256, sum},
		{"unknown-flags", SHA256, concat(sum, ci(1 << 5))},
		{"compression-read-error", SHA256, concat(sum, ci(0))},
		{"unknown-compression", SHA256, concat(sum, ci(0), ci(1))},
		{"optional-count-error", SHA256, concat(sum, ci(FlagOptionalElements), ci(uint64(CompressionZstd)))},
		{"optional-id-error", SHA256, concat(sum, ci(FlagOptionalElements), ci(uint64(CompressionZstd)), ci(1))},
		{"optional-size-error", SHA256, concat(sum, ci(FlagOptionalElements), ci(uint64(CompressionZstd)), ci(1), ci(7))},
		{"optional-data-error", SHA256, concat(sum, ci(FlagOptionalElements), ci(uint64(CompressionZstd)), ci(1), ci(7), ci(5), []byte{'a', 'b'})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ReadPreface(bytes.NewReader(c.data), c.ct); err == nil {
				t.Fatalf("expected error for %s", c.name)
			}
		})
	}
}

func TestPrefaceWriteToErrors(t *testing.T) {
	sum := cksum(t, SHA256, 0x22)

	unknownFlags := &Preface{DataChecksum: sum, Flags: 1 << 6, CompressionType: CompressionZstd}
	if _, err := unknownFlags.WriteTo(io.Discard); err == nil {
		t.Fatal("WriteTo accepted unknown flags")
	}

	unknownComp := &Preface{DataChecksum: sum, CompressionType: CompressionType(1)}
	if _, err := unknownComp.WriteTo(io.Discard); err == nil {
		t.Fatal("WriteTo accepted unknown compression type")
	}

	// Flag set but no elements.
	flagNoElems := &Preface{DataChecksum: sum, Flags: FlagOptionalElements, CompressionType: CompressionNone}
	if _, err := flagNoElems.WriteTo(io.Discard); err == nil {
		t.Fatal("WriteTo accepted flag-set-but-no-elements")
	}

	// Elements present but flag clear.
	elemsNoFlag := &Preface{
		DataChecksum:     sum,
		CompressionType:  CompressionNone,
		OptionalElements: []OptionalElement{{ID: 1, Data: []byte("x")}},
	}
	if _, err := elemsNoFlag.WriteTo(io.Discard); err == nil {
		t.Fatal("WriteTo accepted elements-without-flag")
	}

	// Underlying writer fails.
	good := &Preface{DataChecksum: sum, CompressionType: CompressionZstd}
	if _, err := good.WriteTo(&failWriter{failAt: 1}); !errors.Is(err, errBoom) {
		t.Fatalf("WriteTo write error = %v, want errBoom", err)
	}
}
