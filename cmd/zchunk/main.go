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
	return root
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

// reportLead parses the lead from r and prints its fields.
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
	return nil
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
