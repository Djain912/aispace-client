package cmd

import (
	"errors"
	"io"

	"github.com/spf13/cobra"

	aispmcp "github.com/aispace-sh/aispace-client/internal/mcp"
)

func (a *app) mcpCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "mcp",
		Short: "Run the local Model Context Protocol server",
		Args:  noArgs,
	}
	group.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Serve the ten aispace artifact tools over stdio",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.flagKey != "" {
				return usagef("--key is not accepted by `aispace mcp serve`; inject AISPACE_KEY through the host environment")
			}
			stdin, ok := a.stdin.(io.ReadCloser)
			if !ok {
				stdin = io.NopCloser(a.stdin)
			}
			if err := aispmcp.Run(cmd.Context(), stdin, a.stdout, a.stderr, a.version); err != nil {
				if errors.Is(err, cmd.Context().Err()) {
					return cmd.Context().Err()
				}
				return &codedError{code: "mcp", err: err, exit: ExitGeneric}
			}
			return nil
		},
	})
	return group
}
