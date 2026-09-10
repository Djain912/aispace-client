package mcp

import (
	"context"
	"errors"
	"io"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// Run serves MCP over the supplied newline-delimited stdio streams until EOF
// or cancellation. No ordinary CLI output is written to stdout.
func Run(ctx context.Context, stdin io.ReadCloser, stdout, stderr io.Writer, version string) error {
	server, err := NewServer(version, stderr)
	if err != nil {
		return err
	}
	err = server.Run(ctx, &sdkmcp.IOTransport{Reader: stdin, Writer: nopWriteCloser{stdout}})
	if errors.Is(err, io.EOF) || err != nil && strings.Contains(err.Error(), "closing: EOF") {
		return nil
	}
	return err
}
