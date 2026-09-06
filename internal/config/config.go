// Package config loads and saves the aispace CLI configuration.
//
// Resolution order for every setting is flag > environment > config file > default.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultURL is the public aispace service.
const DefaultURL = "https://aispace.sh"

// Environment variable names.
const (
	EnvKey = "AISPACE_KEY"
	EnvURL = "AISPACE_URL"
)

// File is the on-disk shape of config.json.
type File struct {
	Key string `json:"key,omitempty"`
	URL string `json:"url,omitempty"`
}

// Resolved is the effective configuration after applying precedence.
type Resolved struct {
	Key string
	URL string
	// Path is the config file location that was consulted (it may not exist).
	Path string
	// Warnings are non-fatal issues (e.g. loose file permissions) for stderr.
	Warnings []string
}

// Path returns the config file location, honoring XDG_CONFIG_HOME.
func Path() (string, error) {
	if p := os.Getenv("AISPACE_CONFIG"); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "aispace", "config.json"), nil
}

// Load reads the config file at path. A missing file yields an empty File and
// no error.
func Load(path string) (File, []string, error) {
	var f File
	var warnings []string
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return f, nil, nil
		}
		return f, nil, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, fmt.Sprintf("config file %s has permissions %04o, expected 0600 (run: chmod 600 %s)", path, st.Mode().Perm(), path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, warnings, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return f, warnings, nil
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, warnings, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, warnings, nil
}

// Save writes the config file with 0600 permissions, creating parent
// directories (0700) as needed. It writes atomically via a temp file.
func Save(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	// Rename preserves the temp file mode, but be explicit in case of umask quirks.
	return os.Chmod(path, 0o600)
}

// Resolve applies precedence: flag > env > file > default.
func Resolve(flagKey, flagURL string) (Resolved, error) {
	path, err := Path()
	if err != nil {
		return Resolved{}, err
	}
	file, warnings, err := Load(path)
	if err != nil {
		return Resolved{Path: path}, err
	}
	r := Resolved{Path: path, Warnings: warnings}
	r.Key = first(flagKey, os.Getenv(EnvKey), file.Key)
	r.URL = strings.TrimRight(first(flagURL, os.Getenv(EnvURL), file.URL, DefaultURL), "/")
	return r, nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
