package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

// completionShells are the shells whose scripts this command can generate.
var completionShells = []string{"bash", "zsh", "fish", "powershell"}

func (a *app) completionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <bash|zsh|fish|powershell>",
		Short: "Generate a shell completion script, or install it for the current user",
		Long: "Writes a completion script to stdout so it can be redirected wherever the shell\n" +
			"expects it. `completion install` picks that location automatically for bash, zsh\n" +
			"and fish, and never replaces an existing file unless --force is given.",
		Args:      exactArgs(1, "<bash|zsh|fish|powershell>"),
		ValidArgs: completionShells,
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeCompletion(cmd.Root(), args[0], a.stdout)
		},
	}
	cmd.AddCommand(a.completionInstallCmd())
	return cmd
}

// writeCompletion renders the completion script for one shell.
func writeCompletion(root *cobra.Command, shell string, w io.Writer) error {
	switch shell {
	case "bash":
		return root.GenBashCompletionV2(w, true)
	case "zsh":
		return root.GenZshCompletion(w)
	case "fish":
		return root.GenFishCompletion(w, true)
	case "powershell":
		return root.GenPowerShellCompletionWithDesc(w)
	}
	return usagef("unsupported shell %q (supported: bash, zsh, fish, powershell)", shell)
}

type completionInstall struct {
	Shell string `json:"shell"`
	Path  string `json:"path"`
}

func (a *app) completionInstallCmd() *cobra.Command {
	var dir string
	var force bool
	cmd := &cobra.Command{
		Use:   "install [bash|zsh|fish]",
		Short: "Write the completion script where the shell will find it",
		Long: "Installs the completion script into the per-user completion directory, chosen\n" +
			"from XDG_DATA_HOME or XDG_CONFIG_HOME with the usual defaults. Without an\n" +
			"argument the shell is read from $SHELL.\n\n" +
			"An existing file is never replaced unless --force is given, and nothing outside\n" +
			"the chosen directory is touched. PowerShell is not installable this way because\n" +
			"it loads completions from a profile script; run `aispace completion powershell`\n" +
			"and append the output to your profile instead.",
		Example: "  aispace completion install\n" +
			"  aispace completion install zsh\n" +
			"  aispace completion install bash --dir /etc/bash_completion.d --force",
		Args: func(c *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usagef("expected at most one shell, got %d arguments (see `%s --help`)", len(args), c.CommandPath())
			}
			return nil
		},
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := ""
			if len(args) == 1 {
				shell = args[0]
			} else {
				detected, err := detectShell()
				if err != nil {
					return err
				}
				shell = detected
			}
			if shell == "powershell" {
				return usagef("powershell loads completions from a profile script; run `aispace completion powershell` and append its output to $PROFILE")
			}
			name, err := completionFileName(shell)
			if err != nil {
				return err
			}
			target := dir
			if target == "" {
				if target, err = completionDir(shell); err != nil {
					return err
				}
			}
			path := filepath.Join(target, name)

			// Render fully before touching the filesystem, so a generator error
			// cannot leave a half-written script that the shell would source.
			var script byteWriter
			if err := writeCompletion(cmd.Root(), shell, &script); err != nil {
				return err
			}

			if err := os.MkdirAll(target, 0o755); err != nil {
				return &codedError{code: "io", err: fmt.Errorf("create completion directory: %w", err), exit: ExitGeneric}
			}
			flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
			if force {
				flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}
			f, err := os.OpenFile(path, flags, 0o644)
			if err != nil {
				if os.IsExist(err) {
					return usagef("completion file already exists: %s (pass --force to replace it)", path)
				}
				return &codedError{code: "io", err: fmt.Errorf("write completion: %w", err), exit: ExitGeneric}
			}
			ok := false
			defer func() {
				_ = f.Close()
				if !ok {
					_ = os.Remove(path)
				}
			}()
			if _, err := f.Write(script.b); err != nil {
				return &codedError{code: "io", err: fmt.Errorf("write completion: %w", err), exit: ExitGeneric}
			}
			if err := f.Close(); err != nil {
				return &codedError{code: "io", err: fmt.Errorf("write completion: %w", err), exit: ExitGeneric}
			}
			ok = true

			if a.jsonOut {
				return a.printJSONValue(completionInstall{Shell: shell, Path: path})
			}
			fmt.Fprintf(a.stdout, "installed %s completion %s\n", shell, path)
			if hint := completionHint(shell, target); hint != "" {
				fmt.Fprintln(a.stderr, hint)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory to install into (default: the per-user completion directory)")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing completion file")
	return cmd
}

// byteWriter collects a generated script so it reaches disk only once the whole
// thing rendered without error.
type byteWriter struct{ b []byte }

func (w *byteWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

// detectShell reads $SHELL, which a login shell sets to the user's shell.
func detectShell() (string, error) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		return "", usagef("cannot detect the shell: $SHELL is not set; name it explicitly, e.g. `aispace completion install bash`")
	}
	base := filepath.Base(sh)
	if runtime.GOOS == "windows" {
		base = trimExe(base)
	}
	switch base {
	case "bash", "zsh", "fish":
		return base, nil
	}
	return "", usagef("cannot install completion for %q automatically; name a supported shell (bash, zsh, fish)", base)
}

func trimExe(name string) string {
	if ext := filepath.Ext(name); ext == ".exe" {
		return name[:len(name)-len(ext)]
	}
	return name
}

func completionFileName(shell string) (string, error) {
	switch shell {
	case "bash":
		return "aispace", nil
	case "zsh":
		return "_aispace", nil
	case "fish":
		return "aispace.fish", nil
	}
	return "", usagef("cannot install completion for %q (supported: bash, zsh, fish)", shell)
}

// completionDir is the per-user directory each shell reads completions from.
func completionDir(shell string) (string, error) {
	switch shell {
	case "bash":
		base, err := xdgDir("XDG_DATA_HOME", ".local", "share")
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "bash-completion", "completions"), nil
	case "zsh":
		base, err := xdgDir("XDG_DATA_HOME", ".local", "share")
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "zsh", "site-functions"), nil
	case "fish":
		base, err := xdgDir("XDG_CONFIG_HOME", ".config")
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "fish", "completions"), nil
	}
	return "", usagef("cannot install completion for %q (supported: bash, zsh, fish)", shell)
}

func xdgDir(env string, fallback ...string) (string, error) {
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", &codedError{code: "config", err: fmt.Errorf("cannot determine home directory: %w", err), exit: ExitGeneric}
	}
	return filepath.Join(append([]string{home}, fallback...)...), nil
}

// completionHint names the one manual step a shell still needs, if any.
func completionHint(shell, dir string) string {
	switch shell {
	case "zsh":
		return fmt.Sprintf("note: zsh reads this directory only if it is on $fpath; add to ~/.zshrc if missing:\n  fpath=(%s $fpath)", dir)
	case "bash":
		return "note: bash reads this directory only when the bash-completion package is loaded"
	}
	return ""
}
