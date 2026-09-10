package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const envAllowedRoots = "AISPACE_ALLOWED_ROOTS"

type rootsPolicy struct {
	cwd       string
	explicit  []string
	mu        sync.RWMutex
	host      []string
	hostKnown bool
	hostErr   error
}

func newRootsPolicy() (*rootsPolicy, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}
	cwd, err = canonicalDirectory(cwd)
	if err != nil {
		return nil, err
	}
	p := &rootsPolicy{cwd: cwd}
	if raw, ok := os.LookupEnv(envAllowedRoots); ok {
		for _, item := range filepath.SplitList(raw) {
			if strings.TrimSpace(item) == "" {
				continue
			}
			if !filepath.IsAbs(item) {
				return nil, fmt.Errorf("%s contains a non-absolute path", envAllowedRoots)
			}
			root, err := canonicalDirectory(item)
			if err != nil {
				return nil, fmt.Errorf("%s contains an invalid or inaccessible directory", envAllowedRoots)
			}
			p.explicit = appendUnique(p.explicit, root)
		}
		if len(p.explicit) == 0 {
			return nil, fmt.Errorf("%s does not contain an allowed directory", envAllowedRoots)
		}
	}
	return p, nil
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return filepath.Clean(real), nil
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if samePath(existing, item) {
			return items
		}
	}
	return append(items, item)
}

func (p *rootsPolicy) refreshHost(ctx context.Context, session *sdkmcp.ServerSession) error {
	params := session.InitializeParams()
	if params == nil || params.Capabilities == nil || params.Capabilities.RootsV2 == nil {
		return nil
	}
	res, err := session.ListRoots(ctx, nil)
	if err != nil {
		p.setHostError(errors.New("the MCP host roots could not be read"))
		return err
	}
	var roots []string
	for _, root := range res.Roots {
		u, err := url.Parse(root.URI)
		if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
			err := errors.New("the MCP host supplied an invalid file root")
			p.setHostError(err)
			return err
		}
		path, err := url.PathUnescape(u.Path)
		if err != nil {
			err := errors.New("the MCP host supplied an invalid file root")
			p.setHostError(err)
			return err
		}
		if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
			path = path[1:]
		}
		canonical, err := canonicalDirectory(filepath.FromSlash(path))
		if err != nil {
			err := errors.New("the MCP host supplied an inaccessible file root")
			p.setHostError(err)
			return err
		}
		roots = appendUnique(roots, canonical)
	}
	p.mu.Lock()
	p.host = roots
	p.hostKnown = true
	p.hostErr = nil
	p.mu.Unlock()
	return nil
}

func (p *rootsPolicy) setHostError(err error) {
	p.mu.Lock()
	p.host = nil
	p.hostKnown = true
	p.hostErr = err
	p.mu.Unlock()
}

func (p *rootsPolicy) effective() ([]string, error) {
	p.mu.RLock()
	host := append([]string(nil), p.host...)
	hostKnown := p.hostKnown
	hostErr := p.hostErr
	p.mu.RUnlock()
	if hostErr != nil {
		return nil, hostErr
	}
	if len(p.explicit) == 0 && !hostKnown {
		return []string{p.cwd}, nil
	}
	if len(p.explicit) == 0 && hostKnown {
		if len(host) == 0 {
			return nil, errors.New("the MCP host supplied no filesystem roots")
		}
		return host, nil
	}
	if !hostKnown {
		return append([]string(nil), p.explicit...), nil
	}
	var out []string
	for _, configured := range p.explicit {
		for _, client := range host {
			switch {
			case within(configured, client):
				out = appendUnique(out, configured)
			case within(client, configured):
				out = appendUnique(out, client)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("configured and host-provided roots do not intersect")
	}
	return out, nil
}

func (p *rootsPolicy) uploadPath(raw string) (string, os.FileInfo, error) {
	if raw == "" {
		return "", nil, errors.New("path is required")
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.cwd, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("resolve upload path: %w", err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return "", nil, err
	}
	if err := p.requireAllowed(real); err != nil {
		return "", nil, err
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", nil, err
	}
	if !st.Mode().IsRegular() {
		return "", nil, errors.New("upload path must be a regular file")
	}
	return filepath.Clean(real), st, nil
}

func (p *rootsPolicy) downloadPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("output_path is required")
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.cwd, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if filepath.Base(abs) == "." || filepath.Base(abs) == string(filepath.Separator) {
		return "", errors.New("output_path must name a file")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", errors.New("output parent directory must already exist")
	}
	st, err := os.Stat(parent)
	if err != nil || !st.IsDir() {
		return "", errors.New("output parent directory must already exist")
	}
	dest := filepath.Join(parent, filepath.Base(abs))
	if err := p.requireAllowed(dest); err != nil {
		return "", err
	}
	if _, err := os.Lstat(dest); err == nil {
		real, resolveErr := filepath.EvalSymlinks(dest)
		if resolveErr != nil {
			return "", errors.New("existing output path is unsafe")
		}
		if err := p.requireAllowed(real); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return filepath.Clean(dest), nil
}

func (p *rootsPolicy) requireAllowed(path string) error {
	roots, err := p.effective()
	if err != nil {
		return err
	}
	for _, root := range roots {
		if within(path, root) {
			return nil
		}
	}
	return errors.New("path is outside the allowed filesystem roots")
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) && os.PathSeparator == '\\'
}
