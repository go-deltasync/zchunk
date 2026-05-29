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

- `internal/zchunk`: the `\0ZCK1` lead magic and the unsigned-LEB128
  *compressed integer* codec (`AppendCompressedInt` / `ReadCompressedInt`) that
  the lead, preface and chunk index are built from — at **100 % test coverage**.
- `zchunk info FILE`: recognises a zchunk file by its lead magic.
- `zchunk --version`.

## Install

```bash
go install github.com/go-deltasync/zchunk/cmd/zchunk@latest
```

## Roadmap

1. Lead + preface parsing (checksum type, header checksum, flags).
2. Chunk index (per-chunk digest, compressed/uncompressed lengths).
3. zstd chunk (de)compression via a pure-Go codec.
4. HTTP-range delta download: diff a remote index against a local file and
   fetch only the missing chunks.
5. `-tags=compat` interop tests against the C `zck`/`unzck`/`zck_delta_size`.

## Conventions

Part of the [go-deltasync](https://github.com/go-deltasync) org: single static
binary, no cgo, BSD-3-Clause, cobra CLI, and **100 % test coverage on the
library package** (CI-enforced). See the org docs for details.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
