package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
)

func (a *app) loginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login --key ask_... [--url https://aispace.sh]",
		Short: "Validate a bot key and save it to the config file",
		Long: "Validates the key against GET /v1/whoami and writes it to the config file\n" +
			"(~/.config/aispace/config.json or $XDG_CONFIG_HOME/aispace/config.json, mode 0600).\n" +
			"The key comes from --key or AISPACE_KEY.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.resolve()
			if err != nil {
				return err
			}
			key := a.flagKey
			if key == "" {
				// Allow `AISPACE_KEY=... aispace login`.
				key = cfg.Key
			}
			if key == "" {
				return usagef("--key is required (or set AISPACE_KEY)")
			}
			if !strings.HasPrefix(key, "ask_") {
				return usagef("key must start with ask_")
			}
			if err := validateKey(key); err != nil {
				return err
			}
			if err := validateServerURL(cfg.URL); err != nil {
				return err
			}
			c := api.New(cfg.URL, key, a.userAgent())
			c.Sleep = sleep
			res, err := c.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			file := config.File{Key: key}
			if cfg.URL != config.DefaultURL {
				file.URL = cfg.URL
			}
			if err := config.Save(cfg.Path, file); err != nil {
				return &codedError{code: "config", err: fmt.Errorf("save config: %w", err), exit: ExitGeneric}
			}
			if a.jsonOut {
				a.printJSON(res.Raw)
				return nil
			}
			fmt.Fprintf(a.stdout, "logged in as key %q (%s) for %s\n", res.Value.Key.Name, res.Value.Key.Prefix, res.Value.User.Email)
			fmt.Fprintf(a.stdout, "saved %s\n", cfg.Path)
			return nil
		},
	}
}

func (a *app) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the key name and account email for the configured key",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(res.Raw)
				return nil
			}
			k := res.Value.Key
			if k.RevokedAt != nil {
				return &codedError{code: "key_revoked", err: errors.New("key is revoked"), exit: ExitAuth}
			}
			fmt.Fprintf(a.stdout, "%s (%s) %s used %s of %s\n", k.Name, k.Prefix, res.Value.User.Email, fmtBytes(k.UsedBytes), fmtBytes(k.BudgetBytes))
			return nil
		},
	}
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.jsonOut {
				return a.printJSONValue(map[string]string{"version": a.version, "user_agent": a.userAgent()})
			}
			fmt.Fprintf(a.stdout, "aispace %s\n", a.userAgent()[len("aispace-cli/"):])
			return nil
		},
	}
}
