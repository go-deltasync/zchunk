package zchunk

import (
	"encoding/hex"
	"testing"
)

func TestChecksumTypeSum(t *testing.T) {
	// Known digests of "abc"; SHA-512/128 is the first 16 bytes of SHA-512.
	cases := []struct {
		t    ChecksumType
		want string
	}{
		{SHA1, "a9993e364706816aba3e25717850c26c9cd0d89d"},
		{SHA256, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{SHA512, "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a" +
			"2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"},
		{SHA512128, "ddaf35a193617abacc417349ae204131"},
	}
	for _, c := range cases {
		got, err := c.t.Sum([]byte("abc"))
		if err != nil {
			t.Fatalf("type %d: Sum: %v", uint64(c.t), err)
		}
		size, _ := c.t.Size()
		if len(got) != size {
			t.Fatalf("type %d: digest length %d, want %d", uint64(c.t), len(got), size)
		}
		if hex.EncodeToString(got) != c.want {
			t.Fatalf("type %d: Sum = %x, want %s", uint64(c.t), got, c.want)
		}
	}
}

func TestChecksumTypeSumUnknown(t *testing.T) {
	if _, err := ChecksumType(99).Sum([]byte("abc")); err == nil {
		t.Fatal("Sum accepted an unknown checksum type")
	}
}

func TestChecksumTypeNewHashUnknown(t *testing.T) {
	if _, err := ChecksumType(99).newHash(); err == nil {
		t.Fatal("newHash accepted an unknown checksum type")
	}
}
