# zchunk

A pure-Go, cross-platform toolkit for the [zchunk](https://github.com/zchunk/zchunk)
file format — a content-defined-chunked container that enables **delta
downloads**: a client fetches only the chunks it is missing over HTTP range
requests and recovers the rest from a local copy. zchunk is what Fedora's
DNF/`librepo` uses to ship repository metadata efficiently.

`go-deltasync/zchunk` is a single static binary, cgo-free, and aims for
on-the-wire compatibility with the C `zck` tooling.

> **Status: functional, pre-1.0.** The full read/write path is implemented and
> tested at 100 % coverage: lead/preface/chunk-index/signature parsing and
> serialisation, per-chunk zstd (de)compression against the dictionary,
> whole-file extraction and assembly, delta planning, and HTTP-range delta
> download. Remaining work is interop hardening against the C `zck` tooling
> (`-tags=compat`) and detached-header / data-checksum verification.

## What works today

- `internal/zchunk` (at **100 % test coverage**):
  - the `\0ZCK1` / `\0ZHR1` lead magics;
  - the variable-length *compressed integer* codec
    (`AppendCompressedInt` / `ReadCompressedInt`) — note zchunk's convention is
    the **inverse** of LEB128: the high bit is clear on non-final bytes and set
    on the final one;
  - the `ChecksumType` registry (SHA-1 / SHA-256 / SHA-512 / SHA-512/128) and
    their digest sizes;
  - the **lead** parser/serialiser (`ReadLead` / `Lead.WriteTo`): ID, checksum
    type, header size and header checksum;
  - the **preface** parser/serialiser (`ReadPreface` / `Preface.WriteTo`): data
    checksum, validated flags, compression type and optional elements.
  - the **chunk index** parser/serialiser (`ReadIndex` / `Index.WriteTo`): chunk
    checksum type, per-chunk digests (plus an uncompressed digest when the
    preface sets the uncompressed-source flag) and compressed/uncompressed
    lengths, with chunk 0 read as the dictionary.
  - the **signature section** parser/serialiser (`ReadSignatures` /
    `Signatures.WriteTo`): the signature count, rejecting any non-zero value
    since no signature type is defined yet (matching the reference).
  - the **chunk codec** (`CompressChunk` / `DecompressChunk`): per-chunk
    `none`/`zstd` (de)compression via the pure-Go `klauspost/compress`, including
    raw-dictionary support so chunks can be coded against chunk 0 (the dict),
    matching the reference's `ZSTD_compress_usingDict`.
  - the **checksum registry's hashing** (`ChecksumType.Sum`): SHA-1 / SHA-256 /
    SHA-512 / SHA-512-128 digests (the last being SHA-512 truncated to 16 bytes,
    per the reference);
  - whole-file **extraction** (`Index.Extract`): reads the body, verifies each
    chunk against its index digest, decompresses it against the dictionary
    (chunk 0) and reassembles the original content.
  - whole-file **assembly** (`WriteFile`): emits a complete file (lead, preface,
    index, signatures, body), computing the data checksum over the body and the
    lead's header checksum, so a written file round-trips through the readers and
    `Extract`.
  - **delta planning** (`PlanDelta`): diffs a target index against a local one by
    compressed digest, classifying every target chunk as reusable-from-local or
    must-fetch and reporting the reused/fetched byte totals — the core of the
    HTTP-range delta download.
  - **HTTP-range delta download** (`HTTPRangeReader`, `ReadRemoteHeader`,
    `DownloadDelta`): fetches a remote file's header with two byte-range
    requests, plans the diff against a local copy, then reconstructs the file by
    copying the header verbatim and assembling the body from reused local chunks
    plus range-fetched missing ones — each verified against its index digest.
  - **header-checksum verification** (`Lead.VerifyHeader`): recomputes the
    header digest over the lead (excluding the checksum field) plus the
    preface/index/signatures and matches it against the lead's embedded value,
    so `ReadRemoteHeader` rejects a corrupt or truncated header before planning
    a download around its offsets.
- `zchunk info FILE`: parses and prints a file's lead, preface, index and
  signature count.
- `zchunk extract FILE OUT`: reconstructs a zchunk file's content into OUT.
- `zchunk download [--local FILE] URL OUT`: delta-downloads URL into OUT,
  reusing chunks from a local copy and fetching only the rest over HTTP range.
- `zchunk --version`.

The binary layout follows the canonical `zchunk_format.txt` from the reference
C implementation.

## Install

```bash
go install github.com/go-deltasync/zchunk/cmd/zchunk@latest
```

## Roadmap

1. ~~Lead parsing (checksum type, header size, header checksum).~~ ✓
2. ~~Preface (data checksum, flags, compression type, optional elements).~~ ✓
3. ~~Chunk index (per-chunk digest, compressed/uncompressed lengths) + signatures.~~ ✓
4. ~~zstd chunk (de)compression via a pure-Go codec.~~ ✓
5. ~~HTTP-range delta download: diff a remote index against a local file and
   fetch only the missing chunks.~~ ✓
6. `-tags=compat` interop tests against the C `zck`/`unzck` (in progress: the
   read path verifies `zck`-produced files extract correctly, and the write
   path verifies our files decompress under `unzck`; the CI `compat` workflow
   installs the reference and the tests skip cleanly when it is absent).

## Performance

The library ships micro- and macro-benchmarks for the hot paths. Run the
pure-Go set with:

```bash
go test -run '^$' -bench . -benchmem ./internal/zchunk/
```

Indicative results (Apple M4 Max, zstd default level, 64 KiB chunks for the
codec benchmarks, 1 MiB / 32 KiB-chunk file for `Extract`/`WriteFile`):

| Benchmark             | Throughput  | Allocs/op |
|-----------------------|-------------|-----------|
| `CompressChunkZstd`   | ~330 MB/s   | 37        |
| `DecompressChunkZstd` | ~1.06 GB/s  | 27        |
| `ChecksumSHA256`      | ~3.27 GB/s  | 1         |
| `Extract` (whole file)| ~1.05 GB/s  | 137       |
| `WriteFile`           | ~2.40 GB/s  | 20        |

A `compat`-tagged benchmark compares our in-process `Extract` head-to-head with
the C `unzck` tool on the *same* `zck`-produced file:

```bash
go test -tags=compat -run '^$' -bench BenchmarkCompatExtract ./internal/zchunk/
```

On a 4 MiB compressible input our in-process decode runs at ~2.3 GB/s versus
~0.66 GB/s for shelling out to `unzck` (~3.4× faster end-to-end). The `unzck`
figure includes process-spawn and pipe overhead, so it measures the realistic
cost of invoking the C tool rather than its raw codec speed.

**Implemented optimisations.**

- *Single decoder per file.* `Extract` reuses one zstd decoder bound to the
  file's dictionary across all chunks, instead of constructing one per chunk —
  mirroring the reference's single `ZSTD_DCtx`. This cut a 32-chunk extract from
  908 to 137 allocations/op (and ~3.9 MB → ~1.5 MB/op).
- *Coalesced + concurrent range fetches.* `AssembleBody` merges every run of
  consecutive must-fetch chunks into a single contiguous range request (target
  chunks are laid out back-to-back in the body, so a run is one byte range),
  then fetches the runs in parallel with a bounded worker pool
  (`defaultFetchConcurrency`) while still writing output strictly in source
  order. This cuts both the number of HTTP round-trips and their wall-clock
  latency, as `librepo` does.

**Proposed further improvements** (not yet implemented):

- *Encoder reuse / pooling* on the write path, symmetric to the decoder reuse:
  a high-level file builder should reuse one zstd encoder (e.g. via a
  `sync.Pool`) across chunks rather than the ~1.8 MB/op a fresh
  `CompressChunk` allocates today.
- *Scratch-buffer reuse* in `Extract`/`AssembleBody`: reuse a `[]byte` sized to
  the largest chunk for the compressed read and the decode destination, to
  shave the remaining per-chunk allocations.

## Conventions

Part of the [go-deltasync](https://github.com/go-deltasync) org: single static
binary, no cgo, BSD-3-Clause, cobra CLI, and **100 % test coverage on the
library package** (CI-enforced). See the org docs for details.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
