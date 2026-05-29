# zchunk

A pure-Go, cross-platform toolkit for the [zchunk](https://github.com/zchunk/zchunk)
file format — a content-defined-chunked container that enables **delta
downloads**: a client fetches only the chunks it is missing over HTTP range
requests and recovers the rest from a local copy. zchunk is what Fedora's
DNF/`librepo` uses to ship repository metadata efficiently.

`go-deltasync/zchunk` is a single static binary, cgo-free, and aims for
on-the-wire compatibility with the C `zck` tooling.

> **Status: early scaffolding.** This repo currently provides the format's
> foundational primitives (the lead magic and the variable-length *compressed
> integer* codec) plus a CLI skeleton. The lead/preface/chunk-index parsing,
> zstd handling and HTTP-range delta download are tracked in a dedicated design
> plan and land here incrementally.

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
- `zchunk info FILE`: parses and prints a file's lead and preface.
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
3. Chunk index (per-chunk digest, compressed/uncompressed lengths) + signatures.
4. zstd chunk (de)compression via a pure-Go codec.
5. HTTP-range delta download: diff a remote index against a local file and
   fetch only the missing chunks.
6. `-tags=compat` interop tests against the C `zck`/`unzck`/`zck_delta_size`.

## Conventions

Part of the [go-deltasync](https://github.com/go-deltasync) org: single static
binary, no cgo, BSD-3-Clause, cobra CLI, and **100 % test coverage on the
library package** (CI-enforced). See the org docs for details.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
