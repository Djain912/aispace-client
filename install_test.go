package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerPlatformsVersionsAndDownloaders(t *testing.T) {
	tests := []struct {
		name, os, arch, version, tag, asset, downloader, checksum string
		latest                                                    bool
	}{
		{"darwin-amd64-bare-version-curl", "Darwin", "x86_64", "1.2.3", "v1.2.3", "aispace_darwin_amd64.tar.gz", "curl", "sha256sum", false},
		{"linux-arm64-v-version-wget", "Linux", "aarch64", "v2.0.1", "v2.0.1", "aispace_linux_arm64.tar.gz", "wget", "shasum", false},
		{"linux-amd64-full-version-openssl", "Linux", "amd64", "v3.4.5", "v3.4.5", "aispace_linux_amd64.tar.gz", "curl", "openssl", false},
		{"darwin-arm64-latest", "Darwin", "arm64", "", "v9.8.7", "aispace_darwin_arm64.tar.gz", "curl", "sha256sum", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newInstallerHarness(t, tt.os, tt.arch, tt.downloader, tt.checksum)
			h.addArchive(tt.asset, true)
			if tt.latest {
				h.latest = tt.tag
			}
			result := h.run(tt.version)
			if result.err != nil {
				t.Fatalf("installer failed: %v\n%s", result.err, result.output)
			}
			installed := filepath.Join(h.installDir, "aispace")
			info, err := os.Stat(installed)
			if err != nil {
				t.Fatalf("installed binary: %v", err)
			}
			if info.Mode()&0111 == 0 {
				t.Fatalf("installed binary is not executable: mode %v", info.Mode())
			}
			wantBase := "https://github.com/aispace-sh/aispace-client/releases/download/" + tt.tag
			log := h.readLog()
			for _, want := range []string{tt.downloader + " " + wantBase + "/" + tt.asset, tt.downloader + " " + wantBase + "/checksums.txt", "checksum " + tt.checksum} {
				if !strings.Contains(log, want) {
					t.Errorf("tool log missing %q:\n%s", want, log)
				}
			}
			if tt.latest && !strings.Contains(log, tt.downloader+" https://api.github.com/repos/aispace-sh/aispace-client/releases/latest") {
				t.Errorf("latest release API was not queried:\n%s", log)
			}
		})
	}
}

func TestInstallerFailuresPreserveExistingBinary(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*installerHarness)
		wantOutput string
	}{
		{"checksum-mismatch", func(h *installerHarness) {
			h.addArchive("aispace_linux_amd64.tar.gz", true)
			h.badChecksum = true
		}, "checksum mismatch"},
		{"missing-archive-binary", func(h *installerHarness) { h.addArchive("aispace_linux_amd64.tar.gz", false) }, "archive did not contain aispace"},
		{"download-failure", func(h *installerHarness) { h.failDownload = true }, "download failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newInstallerHarness(t, "Linux", "x86_64", "curl", "sha256sum")
			tt.configure(h)
			oldPath := filepath.Join(h.installDir, "aispace")
			if err := os.WriteFile(oldPath, []byte("existing-install"), 0755); err != nil {
				t.Fatal(err)
			}
			result := h.run("1.0.0")
			if result.err == nil {
				t.Fatalf("installer unexpectedly succeeded:\n%s", result.output)
			}
			if !strings.Contains(result.output, tt.wantOutput) {
				t.Errorf("output missing %q:\n%s", tt.wantOutput, result.output)
			}
			got, err := os.ReadFile(oldPath)
			if err != nil || string(got) != "existing-install" {
				t.Fatalf("existing binary changed: contents=%q err=%v", got, err)
			}
		})
	}
}

type installerHarness struct {
	t            *testing.T
	root, binDir string
	installDir   string
	assetsDir    string
	logPath      string
	os, arch     string
	downloader   string
	checksum     string
	latest       string
	archiveHash  string
	badChecksum  bool
	failDownload bool
}

type installerResult struct {
	output string
	err    error
}

func newInstallerHarness(t *testing.T, goos, arch, downloader, checksum string) *installerHarness {
	t.Helper()
	root := t.TempDir()
	h := &installerHarness{t: t, root: root, binDir: filepath.Join(root, "bin"), installDir: filepath.Join(root, "install"), assetsDir: filepath.Join(root, "assets"), logPath: filepath.Join(root, "tools.log"), os: goos, arch: arch, downloader: downloader, checksum: checksum}
	for _, dir := range []string{h.binDir, h.installDir, h.assetsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"sh", "tar", "gzip", "grep", "sed", "awk", "head", "mktemp", "rm", "chmod", "mv", "mkdir", "cp", "cmp"} {
		h.linkTool(tool)
	}
	h.writeTool("uname", fmt.Sprintf("case \"$1\" in -s) printf '%%s\\n' %q;; -m) printf '%%s\\n' %q;; *) exit 1;; esac\n", goos, arch))
	h.writeDownloader(downloader)
	h.writeTool(checksum, checksumScript(checksum))
	return h
}

func (h *installerHarness) addArchive(asset string, includeBinary bool) {
	h.t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	name := "README"
	mode := int64(0644)
	body := []byte("not the binary")
	if includeBinary {
		name, mode, body = "aispace", 0755, []byte("#!/bin/sh\nprintf 'fixture binary\\n'\n")
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body))}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		h.t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		h.t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.assetsDir, asset), raw.Bytes(), 0644); err != nil {
		h.t.Fatal(err)
	}
	sum := sha256.Sum256(raw.Bytes())
	h.archiveHash = fmt.Sprintf("%x", sum)
	checksums := h.archiveHash + "  " + asset + "\n"
	if err := os.WriteFile(filepath.Join(h.assetsDir, "checksums.txt"), []byte(checksums), 0644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *installerHarness) run(version string) installerResult {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(h.binDir, "sh"), "install.sh")
	cmd.Dir = "."
	hash := h.archiveHash
	if h.badChecksum {
		hash = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	env := []string{
		"PATH=" + h.binDir,
		"HOME=" + h.root,
		"AISPACE_INSTALL_DIR=" + h.installDir,
		"FAKE_ASSETS=" + h.assetsDir,
		"FAKE_LOG=" + h.logPath,
		"FAKE_HASH=" + hash,
		"FAKE_LATEST=" + h.latest,
	}
	if version != "" {
		env = append(env, "AISPACE_VERSION="+version)
	}
	if h.failDownload {
		env = append(env, "FAKE_FAIL_DOWNLOAD=1")
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return installerResult{string(out), err}
}

func (h *installerHarness) readLog() string {
	h.t.Helper()
	b, err := os.ReadFile(h.logPath)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *installerHarness) linkTool(name string) {
	h.t.Helper()
	source, err := exec.LookPath(name)
	if err != nil {
		h.t.Fatalf("required host test tool %s: %v", name, err)
	}
	if err := os.Symlink(source, filepath.Join(h.binDir, name)); err != nil {
		h.t.Fatal(err)
	}
}

func (h *installerHarness) writeTool(name, body string) {
	h.t.Helper()
	contents := "#!/bin/sh\nset -eu\n" + body
	if err := os.WriteFile(filepath.Join(h.binDir, name), []byte(contents), 0755); err != nil {
		h.t.Fatal(err)
	}
}

func (h *installerHarness) writeDownloader(name string) {
	h.t.Helper()
	if name == "curl" {
		h.writeTool(name, `
url=""
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -H) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf 'curl %s\n' "$url" >> "$FAKE_LOG"
if [ "${FAKE_FAIL_DOWNLOAD:-}" = 1 ]; then exit 22; fi
case "$url" in
  *api.github.com*) printf '[{"tag_name":"%s"}]\n' "$FAKE_LATEST" ;;
  *) cp "$FAKE_ASSETS/${url##*/}" "$out" ;;
esac
`)
		return
	}
	h.writeTool(name, `
url=""
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -O) out=$2; shift 2 ;;
    --header=*) shift ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf 'wget %s\n' "$url" >> "$FAKE_LOG"
if [ "${FAKE_FAIL_DOWNLOAD:-}" = 1 ]; then exit 8; fi
case "$url" in
  *api.github.com*) printf '[{"tag_name":"%s"}]\n' "$FAKE_LATEST" ;;
  *) cp "$FAKE_ASSETS/${url##*/}" "$out" ;;
esac
`)
}

func checksumScript(name string) string {
	switch name {
	case "sha256sum":
		return `printf 'checksum sha256sum\n' >> "$FAKE_LOG"
file=$1
cmp -s "$file" "$FAKE_ASSETS/${file##*/}" || FAKE_HASH=corrupt
printf '%s  %s\n' "$FAKE_HASH" "$file"
`
	case "shasum":
		return `printf 'checksum shasum\n' >> "$FAKE_LOG"
file=$3
cmp -s "$file" "$FAKE_ASSETS/${file##*/}" || FAKE_HASH=corrupt
printf '%s  %s\n' "$FAKE_HASH" "$file"
`
	default:
		return `printf 'checksum openssl\n' >> "$FAKE_LOG"
file=$3
cmp -s "$file" "$FAKE_ASSETS/${file##*/}" || FAKE_HASH=corrupt
printf 'SHA256(%s)= %s\n' "$file" "$FAKE_HASH"
`
	}
}
