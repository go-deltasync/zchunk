// zchunk is a pure-Go, cross-platform toolkit for the zchunk file format — a
// content-defined-chunked container that supports delta downloads over HTTP
// range requests (as used by Fedora's DNF/librepo).
//
// This binary is in early scaffolding: it currently exposes the build version
// and an `info` command that recognises a zchunk file by its lead magic. The
// chunk index, zstd handling and range-based delta download land here as the
// format work progresses.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/go-deltasync/zchunk/internal/zchunk"
	"github.com/spf13/cobra"
)

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRoot().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "zchunk: %v\n", err)
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "zchunk",
		Short:         "Pure-Go tooling for the zchunk delta-download format",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(infoCmd())
	root.AddCommand(extractCmd())
	root.AddCommand(downloadCmd())
	root.AddCommand(headerCmd())
	return root
}

func headerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "header FILE OUT",
		Short: "Write FILE's header on its own as a detached header",
		Long: "Read the full zchunk file FILE and emit just its header to OUT as a " +
			"standalone detached header (lead ID \"\\0ZHR1\"), so a client can fetch " +
			"the small header by itself and learn the chunk layout before delta-" +
			"downloading the body. The header bytes are identical to the embedded " +
			"header except for the 5-byte magic, so the embedded checksum still " +
			"verifies.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeDetachedHeader(args[0], args[1])
		},
	}
}

// writeDetachedHeader reads the header region of the full zchunk file at inPath
// and writes it to outPath with the lead magic swapped from "\0ZCK1" to
// "\0ZHR1" and the body omitted — a detached header.
func writeDetachedHeader(inPath, outPath string) error {
	f, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", inPath, err)
	}
	defer f.Close()
	lead, err := zchunk.ReadLead(f)
	if err != nil {
		return err
	}
	leadLen, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	header := make([]byte, leadLen+int64(lead.HeaderSize))
	if _, err := f.ReadAt(header, 0); err != nil {
		return fmt.Errorf("read header of %s: %w", inPath, err)
	}
	// Swap the 5-byte embedded magic for the detached one; the checksum, which
	// is computed with the embedded magic regardless, stays valid.
	copy(header[:len(zchunk.DetachedMagic)], zchunk.DetachedMagic)

	if err := os.WriteFile(outPath, header, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

func downloadCmd() *cobra.Command {
	var localPath string
	cmd := &cobra.Command{
		Use:   "download URL OUT",
		Short: "Delta-download URL into OUT, reusing chunks from a local copy",
		Long: "Fetch the remote zchunk file at URL into OUT over HTTP range requests, " +
			"reusing any chunks already present in the --local copy and fetching only the rest.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			url, outPath := args[0], args[1]

			var localIndex *zchunk.Index
			var localBody io.ReaderAt = bytes.NewReader(nil)
			if localPath != "" {
				idx, body, closeLocal, err := openLocal(localPath)
				if err != nil {
					return err
				}
				defer closeLocal()
				localIndex, localBody = idx, body
			}

			out, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("create %s: %w", outPath, err)
			}
			defer out.Close()

			remote := zchunk.NewHTTPRangeReader(url, 0, nil)
			if _, err := zchunk.DownloadDelta(remote, localIndex, localBody, out); err != nil {
				return err
			}
			return out.Close()
		},
	}
	cmd.Flags().StringVar(&localPath, "local", "", "existing local zchunk file to reuse chunks from")
	return cmd
}

// openLocal opens a local zchunk file and returns its index and a ReaderAt over
// its body (the bytes following the header), plus a close function.
func openLocal(path string) (*zchunk.Index, io.ReaderAt, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	lead, err := zchunk.ReadLead(f)
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	pre, err := zchunk.ReadPreface(f, lead.ChecksumType)
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	idx, err := zchunk.ReadIndex(f, pre.UncompressedSource())
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	if _, err := zchunk.ReadSignatures(f); err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	off, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, nil, err
	}
	return idx, io.NewSectionReader(f, off, fi.Size()-off), f.Close, nil
}

func extractCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "extract FILE OUT",
		Short: "Decompress FILE's chunks and write the content to OUT",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[0], err)
			}
			defer f.Close()
			out, err := os.Create(args[1])
			if err != nil {
				return fmt.Errorf("create %s: %w", args[1], err)
			}
			defer out.Close()
			return extract(f, out)
		},
	}
}

// extract parses the full header from r, then reconstructs the file content into
// out.
func extract(r io.Reader, out io.Writer) error {
	lead, err := zchunk.ReadLead(r)
	if err != nil {
		return err
	}
	pre, err := zchunk.ReadPreface(r, lead.ChecksumType)
	if err != nil {
		return err
	}
	idx, err := zchunk.ReadIndex(r, pre.UncompressedSource())
	if err != nil {
		return err
	}
	if _, err := zchunk.ReadSignatures(r); err != nil {
		return err
	}
	_, err = idx.Extract(r, pre.CompressionType, out)
	return err
}

func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info FILE",
		Short: "Parse and report FILE's zchunk lead",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[0], err)
			}
			defer f.Close()
			return reportLead(cmd.OutOrStdout(), args[0], f)
		},
	}
}

// reportLead parses the lead and preface from r and prints their fields.
func reportLead(out io.Writer, name string, r io.Reader) error {
	lead, err := zchunk.ReadLead(r)
	if err != nil {
		return err
	}
	kind := "zchunk file"
	if lead.Detached {
		kind = "detached zchunk header"
	}
	fmt.Fprintf(out, "%s: %s\n", name, kind)
	fmt.Fprintf(out, "  checksum type: %s\n", checksumName(lead.ChecksumType))
	fmt.Fprintf(out, "  header size:   %d bytes\n", lead.HeaderSize)
	fmt.Fprintf(out, "  header cksum:  %x\n", lead.HeaderChecksum)

	pre, err := zchunk.ReadPreface(r, lead.ChecksumType)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  compression:   %s\n", compressionName(pre.CompressionType))
	fmt.Fprintf(out, "  flags:         %#x (streams=%t optional=%t uncompressed=%t)\n",
		pre.Flags, pre.HasDataStreams(), pre.HasOptionalElements(), pre.UncompressedSource())
	fmt.Fprintf(out, "  data cksum:    %x\n", pre.DataChecksum)
	if n := len(pre.OptionalElements); n > 0 {
		fmt.Fprintf(out, "  optional elts: %d\n", n)
	}

	idx, err := zchunk.ReadIndex(r, pre.UncompressedSource())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  chunk cksum:   %s\n", checksumName(idx.ChunkChecksumType))
	fmt.Fprintf(out, "  chunks:        %d", len(idx.Chunks))
	if d, ok := idx.Dict(); ok {
		fmt.Fprintf(out, " (dict: %d -> %d bytes)", d.CompLength, d.Length)
	}
	fmt.Fprintln(out)

	sigs, err := zchunk.ReadSignatures(r)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  signatures:    %d\n", sigs.Count)
	return nil
}

func compressionName(c zchunk.CompressionType) string {
	switch c {
	case zchunk.CompressionNone:
		return "none"
	case zchunk.CompressionZstd:
		return "zstd"
	default:
		return fmt.Sprintf("unknown(%d)", uint64(c))
	}
}

func checksumName(t zchunk.ChecksumType) string {
	switch t {
	case zchunk.SHA1:
		return "SHA-1"
	case zchunk.SHA256:
		return "SHA-256"
	case zchunk.SHA512:
		return "SHA-512"
	case zchunk.SHA512128:
		return "SHA-512/128"
	default:
		return fmt.Sprintf("unknown(%d)", uint64(t))
	}
}
