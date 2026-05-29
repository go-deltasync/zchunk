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
		Short: "Report whether FILE has a valid zchunk lead magic",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open %s: %w", args[0], err)
			}
			defer f.Close()
			return reportMagic(cmd.OutOrStdout(), args[0], f)
		},
	}
}

// reportMagic reads the lead magic from r and prints whether it matches.
func reportMagic(out io.Writer, name string, r io.Reader) error {
	buf := make([]byte, len(zchunk.Magic))
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if string(buf) != zchunk.Magic {
		return fmt.Errorf("%s: not a zchunk file (bad lead magic)", name)
	}
	fmt.Fprintf(out, "%s: zchunk file (lead magic OK)\n", name)
	return nil
}
