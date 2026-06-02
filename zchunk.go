// Package zchunk is a pure-Go, cgo-free toolkit for the zchunk file format
// (content-defined chunking with per-chunk checksums, enabling HTTP-Range delta
// updates), interoperable with the reference zchunk/unzck tools.
//
// It re-exports the building blocks used to create archives (Builder), read
// their structure (ReadLead/ReadPreface/ReadIndex and the Index/Preface/Lead
// types), and perform delta downloads (PlanDelta, DownloadDelta, RangeReader,
// NewHTTPRangeReader).
package zchunk

import (
	impl "github.com/go-deltasync/zchunk/internal/zchunk"
)

// Format types.
type (
	Builder         = impl.Builder
	Index           = impl.Index
	IndexEntry      = impl.IndexEntry
	Preface         = impl.Preface
	Lead            = impl.Lead
	ChecksumType    = impl.ChecksumType
	CompressionType = impl.CompressionType
	DeltaPlan       = impl.DeltaPlan
	RangeReader     = impl.RangeReader
	RemoteHeader    = impl.RemoteHeader
	HTTPRangeReader = impl.HTTPRangeReader
	Signatures      = impl.Signatures
	ChunkSource     = impl.ChunkSource
	OptionalElement = impl.OptionalElement
)

// Checksum types.
const (
	SHA1      = impl.SHA1
	SHA256    = impl.SHA256
	SHA512    = impl.SHA512
	SHA512128 = impl.SHA512128
)

// Compression types.
const (
	CompressionNone = impl.CompressionNone
	CompressionZstd = impl.CompressionZstd
)

// File magics.
const (
	Magic         = impl.Magic
	DetachedMagic = impl.DetachedMagic
)

// Re-exported functions (the internal package is the single source of truth).
var (
	NewBuilder              = impl.NewBuilder
	GenerateDict            = impl.GenerateDict
	CompressChunk           = impl.CompressChunk
	DecompressChunk         = impl.DecompressChunk
	ReadIndex               = impl.ReadIndex
	ReadLead                = impl.ReadLead
	ReadPreface             = impl.ReadPreface
	ReadDetachedHeader      = impl.ReadDetachedHeader
	ReadRemoteHeader        = impl.ReadRemoteHeader
	ReadSignatures          = impl.ReadSignatures
	WriteFile               = impl.WriteFile
	WriteDetachedHeader     = impl.WriteDetachedHeader
	PlanDelta               = impl.PlanDelta
	DownloadDelta           = impl.DownloadDelta
	DownloadDeltaWithHeader = impl.DownloadDeltaWithHeader
	NewHTTPRangeReader      = impl.NewHTTPRangeReader
)
