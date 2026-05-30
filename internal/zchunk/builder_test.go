package zchunk

import (
	"bytes"
	"testing"
)

func TestBuilderRoundTrip(t *testing.T) {
	dict := bytes.Repeat([]byte("dictionary words "), 16)
	chunks := [][]byte{
		append(append([]byte(nil), dict...), []byte(" first chunk tail")...),
		[]byte("a second, entirely different chunk payload here"),
		{}, // an empty chunk
	}
	cases := []struct {
		name string
		ct   CompressionType
		dict []byte
	}{
		{"zstd-dict", CompressionZstd, dict},
		{"zstd-nodict", CompressionZstd, nil},
		{"none-dict", CompressionNone, dict},
		{"none-nodict", CompressionNone, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := NewBuilder(c.ct, SHA256, c.dict)
			if err != nil {
				t.Fatalf("NewBuilder: %v", err)
			}
			defer b.Close()
			for _, plain := range chunks {
				b.AddChunk(plain)
			}

			// The index has chunk 0 (dict) plus one entry per data chunk.
			if got, want := len(b.Index().Chunks), len(chunks)+1; got != want {
				t.Fatalf("index chunks = %d, want %d", got, want)
			}

			var buf bytes.Buffer
			pre := &Preface{CompressionType: c.ct}
			n, err := b.WriteFile(&buf, SHA256, pre, nil)
			if err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if int(n) != buf.Len() {
				t.Fatalf("WriteFile n=%d, buffer=%d", n, buf.Len())
			}

			// Read the file back and extract: the content must be the chunks
			// concatenated (the dictionary is not part of the output content).
			r := bytes.NewReader(buf.Bytes())
			lead, err := ReadLead(r)
			if err != nil {
				t.Fatalf("ReadLead: %v", err)
			}
			pre2, err := ReadPreface(r, lead.ChecksumType)
			if err != nil {
				t.Fatalf("ReadPreface: %v", err)
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
			for _, plain := range chunks {
				want.Write(plain)
			}
			if !bytes.Equal(out.Bytes(), want.Bytes()) {
				t.Fatal("round-tripped content mismatch")
			}
		})
	}
}

func TestBuilderBodyMatchesManual(t *testing.T) {
	// A Builder with an empty dict must produce the same body bytes as the
	// per-chunk CompressChunk path it replaces.
	data := [][]byte{[]byte("alpha beta gamma"), []byte("delta epsilon zeta")}
	b, err := NewBuilder(CompressionZstd, SHA256, nil)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	defer b.Close()
	for _, d := range data {
		b.AddChunk(d)
	}

	var want []byte
	for _, d := range data {
		comp, err := CompressChunk(CompressionZstd, nil, d)
		if err != nil {
			t.Fatalf("CompressChunk: %v", err)
		}
		want = append(want, comp...)
	}
	if !bytes.Equal(b.Body(), want) {
		t.Fatal("Builder body differs from per-chunk CompressChunk output")
	}
}

func TestNewBuilderErrors(t *testing.T) {
	if _, err := NewBuilder(99, SHA256, nil); err == nil {
		t.Fatal("NewBuilder accepted an unknown compression type")
	}
	if _, err := NewBuilder(CompressionZstd, 99, nil); err == nil {
		t.Fatal("NewBuilder accepted an unknown chunk checksum type")
	}
}
