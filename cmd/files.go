package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/duration"
)

func (a *app) linkCmd() *cobra.Command {
	var expires string
	var maxDownloads int64
	cmd := &cobra.Command{
		Use:   "link <file_id> [--expires 1h] [--max-downloads N]",
		Short: "Create a share link for a file; the URL is printed last on its own line",
		Args:  exactArgs(1, "<file_id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if maxDownloads < 0 {
				return usagef("--max-downloads must be >= 0")
			}
			var expiresIn int64
			if expires != "" {
				var err error
				if expiresIn, err = duration.Seconds(expires); err != nil {
					return usagef("--expires: %v", err)
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.CreateLink(cmd.Context(), args[0], api.LinkOptions{ExpiresIn: expiresIn, MaxDownloads: maxDownloads})
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(res.Raw)
				return nil
			}
			a.printLinkLine(res.Value)
			fmt.Fprintln(a.stdout, res.Value.URL)
			return nil
		},
	}
	cmd.Flags().StringVar(&expires, "expires", "", "link lifetime, e.g. 30m, 1h, 2d (default: server default, 1h)")
	cmd.Flags().Int64Var(&maxDownloads, "max-downloads", 0, "download cap (default: unlimited)")
	return cmd
}

func (a *app) lsCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List files uploaded with this key (all pages)",
		Long:  "Lists every file for the key, following pagination. Human output is one line per file:\n<id> <size> <expires> <name>. With --json prints {\"files\": [...], \"next_cursor\": null} merged across pages.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if a.jsonOut {
				raws, _, err := c.ListAllFiles(cmd.Context())
				if err != nil {
					return err
				}
				if raws == nil {
					raws = []json.RawMessage{}
				}
				return a.printJSONValue(struct {
					Files      []json.RawMessage `json:"files"`
					NextCursor *string           `json:"next_cursor"`
				}{Files: raws})
			}
			count := 0
			err = c.WalkFiles(cmd.Context(), func(_ []json.RawMessage, files []api.File) error {
				for _, f := range files {
					fmt.Fprintf(a.stdout, "%s %s %s %s\n", f.ID, fmtBytes(f.SizeBytes), fmtTime(f.ExpiresAt), f.Name)
					count++
				}
				return nil
			})
			if err != nil {
				return err
			}
			if count == 0 {
				fmt.Fprintln(a.stderr, "no files")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "accepted for compatibility; ls always follows pagination to the end")
	return cmd
}

func (a *app) infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <file_id>",
		Short: "Show file metadata",
		Args:  exactArgs(1, "<file_id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.GetFile(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(res.Raw)
				return nil
			}
			f := res.Value
			sum := f.SHA256
			if sum == "" {
				sum = "-"
			}
			enc := f.EncAlg
			if enc == "" {
				enc = "none"
			}
			fmt.Fprintf(a.stdout, "%s %s %s %s created %s expires %s sha256 %s encryption %s\n", f.ID, f.Name, fmtBytes(f.SizeBytes), f.ContentType, fmtTime(f.CreatedAt), fmtTime(f.ExpiresAt), sum, enc)
			return nil
		},
	}
}

func (a *app) linksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "links <file_id>",
		Short: "List share links for a file",
		Long:  "Lists share links for a file, one per line: <id> <expires> <downloads>/<max> [revoked].\nURLs are only available at creation time (see `aispace link`).",
		Args:  exactArgs(1, "<file_id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.ListLinks(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(res.Raw)
				return nil
			}
			if len(res.Value.Links) == 0 {
				fmt.Fprintln(a.stderr, "no links")
				return nil
			}
			for _, l := range res.Value.Links {
				max := "unlimited"
				if l.MaxDownloads != nil {
					max = fmt.Sprintf("%d", *l.MaxDownloads)
				}
				state := ""
				if l.RevokedAt != nil {
					state = " revoked"
				}
				fmt.Fprintf(a.stdout, "%s expires %s downloads %d/%s%s\n", l.ID, fmtTime(l.ExpiresAt), l.DownloadCount, max, state)
			}
			return nil
		},
	}
}

func (a *app) rmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <file_id>...",
		Short: "Delete one or more files (revokes all their links)",
		Long:  "Deletes files in order and stops at the first failure. With --json prints one {\"deleted\": id} object per line.",
		Args:  minArgs(1, "<file_id>..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, id := range args {
				if err := c.DeleteFile(cmd.Context(), id); err != nil {
					return prefixID(id, len(args) > 1, err)
				}
				if a.jsonOut {
					if err := a.printJSONValue(map[string]any{"deleted": id}); err != nil {
						return err
					}
					continue
				}
				fmt.Fprintf(a.stdout, "deleted %s\n", id)
			}
			return nil
		},
	}
}

func (a *app) revokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <link_id>...",
		Short: "Revoke one or more share links",
		Long:  "Revokes links in order and stops at the first failure. With --json prints one {\"revoked\": id} object per line.",
		Args:  minArgs(1, "<link_id>..."),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, id := range args {
				if err := c.RevokeLink(cmd.Context(), id); err != nil {
					return prefixID(id, len(args) > 1, err)
				}
				if a.jsonOut {
					if err := a.printJSONValue(map[string]any{"revoked": id}); err != nil {
						return err
					}
					continue
				}
				fmt.Fprintf(a.stdout, "revoked %s\n", id)
			}
			return nil
		},
	}
}

func (a *app) quotaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "quota",
		Short: "Show key budget, account allowance, limits and rate remaining",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			res, err := c.Quota(cmd.Context())
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(res.Raw)
				return nil
			}
			q := res.Value
			fmt.Fprintf(a.stdout, "key: used %s of %s, %s remaining\n", fmtBytes(q.Key.UsedBytes), fmtBytes(q.Key.BudgetBytes), fmtBytes(q.Key.RemainingBytes))
			fmt.Fprintf(a.stdout, "account: used %s of %s, %s remaining, plan %s (extra blocks %d)\n", fmtBytes(q.Account.UsedBytes), fmtBytes(q.Account.AllowanceBytes), fmtBytes(q.Account.RemainingBytes), q.Account.Plan, q.Account.ExtraBlocks)
			fmt.Fprintf(a.stdout, "limits: max file %s, max file ttl %s, max link ttl %s, uploads %d/h %d/d, requests %d/min\n", fmtBytes(q.Limits.MaxFileBytes), fmtSeconds(q.Limits.MaxFileTTLSeconds), fmtSeconds(q.Limits.MaxLinkTTLSeconds), q.Limits.UploadsPerHour, q.Limits.UploadsPerDay, q.Limits.RequestsPerMinute)
			fmt.Fprintf(a.stdout, "rate: uploads remaining %d this hour, %d today; requests remaining %d this minute\n", q.Rate.UploadsHourRemaining, q.Rate.UploadsDayRemaining, q.Rate.RequestsMinuteRemaining)
			return nil
		},
	}
}

// prefixID annotates an API error with the ID it concerns when several were
// given, keeping the error type (and therefore the exit code) intact.
func prefixID(id string, multi bool, err error) error {
	if !multi {
		return err
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		cp := *ae
		cp.Message = id + ": " + ae.Message
		return &cp
	}
	return fmt.Errorf("%s: %w", id, err)
}

func fmtSeconds(s int64) string {
	switch {
	case s%86400 == 0 && s != 0:
		return fmt.Sprintf("%dd", s/86400)
	case s%3600 == 0 && s != 0:
		return fmt.Sprintf("%dh", s/3600)
	case s%60 == 0 && s != 0:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%ds", s)
}
