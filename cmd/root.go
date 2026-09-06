// Package cmd implements the aispace CLI commands.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/aispace-sh/aispace-client/internal/api"
	"github.com/aispace-sh/aispace-client/internal/config"
)

// Exit codes.
const (
	ExitOK        = 0
	ExitGeneric   = 1
	ExitUsage     = 2
	ExitAuth      = 3
	ExitQuota     = 4
	ExitRateLimit = 5
)

// sleep is used by the API client when retrying rate-limited GETs; tests override it.
var sleep func(time.Duration)

// app carries global flag state and IO for one invocation.
type app struct {
	version string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer

	flagKey  string
	flagURL  string
	jsonOut  bool
	cfg      config.Resolved
	resolved bool
}

// usageError marks errors that should map to exit code 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// codedError is a local (non-API) error with a code for the `error: msg (code)` line.
type codedError struct {
	code string
	err  error
	exit int
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

// Run executes the CLI with args and returns the process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	a := &app{version: version, stdin: stdin, stdout: stdout, stderr: stderr}
	root := a.newRootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	code, apiCode := a.classify(err)
	a.printError(err, apiCode)
	return code
}

func (a *app) newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "aispace",
		Short:         "Bot-friendly file drop: upload files and mint expiring share links",
		Long:          "aispace uploads files to aispace.sh under a bot key and mints expiring share links.\nDesigned to be driven by LLM agents: terse output, --json for exact API payloads, stable exit codes.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef("unknown command %q (see `aispace --help`)", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("")
	root.PersistentFlags().StringVar(&a.flagKey, "key", "", "bot API key (ask_...); overrides AISPACE_KEY and the config file")
	root.PersistentFlags().StringVar(&a.flagURL, "url", "", "server base URL; overrides AISPACE_URL and the config file (default https://aispace.sh)")
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "print exactly the API JSON (errors as JSON on stderr)")
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usagef("%v (see `%s --help`)", err, cmd.CommandPath())
	})

	root.AddCommand(
		a.loginCmd(),
		a.uploadCmd(),
		a.decryptCmd(),
		a.linkCmd(),
		a.lsCmd(),
		a.infoCmd(),
		a.linksCmd(),
		a.rmCmd(),
		a.revokeCmd(),
		a.quotaCmd(),
		a.whoamiCmd(),
		a.versionCmd(),
	)
	return root
}

// minArgs is cobra.MinimumNArgs but yields a usage error (exit 2).
func minArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return usagef("expected %s, got %d argument(s) (see `%s --help`)", what, len(args), cmd.CommandPath())
		}
		return nil
	}
}

// exactArgs is cobra.ExactArgs but yields a usage error (exit 2).
func exactArgs(n int, what string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usagef("expected %s, got %d argument(s) (see `%s --help`)", what, len(args), cmd.CommandPath())
		}
		return nil
	}
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("%s takes no arguments (see `%s --help`)", cmd.Name(), cmd.CommandPath())
	}
	return nil
}

// resolve loads configuration (flag > env > file) once per invocation and
// emits permission warnings.
func (a *app) resolve() (config.Resolved, error) {
	if a.resolved {
		return a.cfg, nil
	}
	cfg, err := config.Resolve(a.flagKey, a.flagURL)
	if err != nil {
		return cfg, &codedError{code: "config", err: err, exit: ExitGeneric}
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintf(a.stderr, "warning: %s\n", w)
	}
	a.cfg = cfg
	a.resolved = true
	return cfg, nil
}

func (a *app) userAgent() string {
	return fmt.Sprintf("aispace-cli/%s (%s/%s)", a.version, runtime.GOOS, runtime.GOARCH)
}

// client builds an API client from the resolved configuration.
func (a *app) client() (*api.Client, error) {
	cfg, err := a.resolve()
	if err != nil {
		return nil, err
	}
	if cfg.Key == "" {
		return nil, &codedError{
			code: "unauthenticated",
			err:  errors.New("no API key configured: run `aispace login --key ask_...` or set AISPACE_KEY"),
			exit: ExitAuth,
		}
	}
	if err := validateServerURL(cfg.URL); err != nil {
		return nil, err
	}
	c := api.New(cfg.URL, cfg.Key, a.userAgent())
	c.Sleep = sleep
	return c, nil
}

// validateServerURL prevents bearer keys crossing a cleartext network. HTTP is
// retained for loopback development only.
func validateServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return usagef("--url must be a valid https:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return usagef("--url must be an origin without credentials, path, query, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme == "http" && (host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return usagef("--url must use HTTPS (HTTP is allowed only for localhost)")
}

// classify maps an error to (exit code, error code string).
func (a *app) classify(err error) (int, string) {
	var ue *usageError
	if errors.As(err, &ue) {
		return ExitUsage, "usage"
	}
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.exit, ce.code
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.ExitCode(), ae.Code
	}
	if errors.Is(err, context.Canceled) {
		return ExitGeneric, "interrupted"
	}
	return ExitGeneric, "error"
}

func (a *app) printError(err error, code string) {
	msg := err.Error()
	if a.jsonOut {
		var ae *api.Error
		payload := map[string]any{"code": code, "message": msg}
		if errors.As(err, &ae) {
			if ae.Status != 0 {
				payload["status"] = ae.Status
			}
			if len(ae.Details) > 0 {
				payload["details"] = ae.Details
			}
			if ae.RetryAfter > 0 {
				payload["retry_after"] = ae.RetryAfter
			}
		}
		exit, _ := a.classify(err)
		payload["exit_code"] = exit
		b, _ := json.Marshal(map[string]any{"error": payload})
		fmt.Fprintln(a.stderr, string(b))
		return
	}
	fmt.Fprintf(a.stderr, "error: %s (%s)\n", msg, code)
}

// printJSON writes raw JSON followed by a newline.
func (a *app) printJSON(raw json.RawMessage) {
	fmt.Fprintln(a.stdout, string(raw))
}

// printJSONValue marshals v and prints it.
func (a *app) printJSONValue(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, string(b))
	return nil
}

func fmtTime(unix int64) string {
	if unix == 0 {
		return "-"
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
