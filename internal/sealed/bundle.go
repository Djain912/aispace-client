package sealed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type PlannedBundle struct {
	Files           []PlannedFile
	PlaintextBytes  int64
	CiphertextBytes int64
	ChunkCount      uint64
}

type PlannedFile struct {
	SourcePath string
	ManifestFile
}

// InspectInputs validates and hashes regular files before quota reservation.
func InspectInputs(paths []string) (PlannedBundle, error) {
	var out PlannedBundle
	if len(paths) == 0 || len(paths) > MaxFiles {
		return out, errors.New("sealed transfer needs between 1 and 1000 files")
	}
	seen := make(map[string]struct{}, len(paths))
	var global uint64
	for i, source := range paths {
		st, err := os.Stat(source)
		if err != nil {
			return out, fmt.Errorf("inspect %s: %w", source, err)
		}
		if !st.Mode().IsRegular() {
			return out, fmt.Errorf("%s is not a regular file", source)
		}
		name := filepath.Base(filepath.Clean(source))
		if err := validatePath(name); err != nil {
			return out, fmt.Errorf("%s: %w", source, err)
		}
		if _, ok := seen[name]; ok {
			return out, fmt.Errorf("multiple inputs have the destination name %q", name)
		}
		seen[name] = struct{}{}
		digest, err := hashFile(source)
		if err != nil {
			return out, err
		}
		chunks := chunksForSize(st.Size(), &global)
		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		out.Files = append(out.Files, PlannedFile{
			SourcePath: source,
			ManifestFile: ManifestFile{
				Index: i, Path: filepath.ToSlash(name), ContentType: ct,
				SizeBytes: st.Size(), SHA256: digest, Chunks: chunks,
			},
		})
		out.PlaintextBytes += st.Size()
		out.CiphertextBytes += st.Size() + int64(len(chunks))*gcmTagSize
	}
	out.ChunkCount = global
	return out, nil
}

func chunksForSize(size int64, global *uint64) []ManifestChunk {
	if size == 0 {
		chunk := ManifestChunk{GlobalIndex: *global, PlaintextBytes: 0}
		*global++
		return []ManifestChunk{chunk}
	}
	chunks := make([]ManifestChunk, 0, (size+DefaultChunkSize-1)/DefaultChunkSize)
	for remaining := size; remaining > 0; {
		n := min(remaining, DefaultChunkSize)
		chunks = append(chunks, ManifestChunk{GlobalIndex: *global, PlaintextBytes: n})
		*global++
		remaining -= n
	}
	return chunks
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EncryptBundle writes concatenated authenticated chunks and returns their
// aggregate ciphertext digest. Inputs are re-hashed while encrypting so a
// file changed after inspection cannot silently invalidate the manifest.
func EncryptBundle(ctx context.Context, plan PlannedBundle, master [32]byte, transferID string, dst io.Writer, progress func(int64)) (string, error) {
	h := sha256.New()
	out := io.MultiWriter(dst, h)
	buf := make([]byte, DefaultChunkSize)
	var written int64
	for _, file := range plan.Files {
		in, err := os.Open(file.SourcePath)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", file.SourcePath, err)
		}
		plainHash := sha256.New()
		for chunkIndex, chunk := range file.Chunks {
			if err := ctx.Err(); err != nil {
				_ = in.Close()
				return "", err
			}
			plain := buf[:chunk.PlaintextBytes]
			if _, err := io.ReadFull(in, plain); err != nil {
				_ = in.Close()
				return "", fmt.Errorf("read %s: file changed after inspection: %w", file.SourcePath, err)
			}
			_, _ = plainHash.Write(plain)
			ciphertext, err := EncryptChunk(master, transferID, file.Index, chunk.GlobalIndex, chunkIndex, plain)
			if err != nil {
				_ = in.Close()
				return "", err
			}
			if _, err := out.Write(ciphertext); err != nil {
				_ = in.Close()
				return "", err
			}
			written += int64(len(ciphertext))
			if progress != nil {
				progress(written)
			}
		}
		var one [1]byte
		if n, err := in.Read(one[:]); n != 0 || err != io.EOF {
			_ = in.Close()
			return "", fmt.Errorf("%s changed after inspection", file.SourcePath)
		}
		if err := in.Close(); err != nil {
			return "", err
		}
		if got := hex.EncodeToString(plainHash.Sum(nil)); got != file.SHA256 {
			return "", fmt.Errorf("%s changed after inspection", file.SourcePath)
		}
	}
	if written != plan.CiphertextBytes {
		return "", fmt.Errorf("encrypted %d bytes, expected %d", written, plan.CiphertextBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ManifestFor(plan PlannedBundle, transferID string, createdAt, expiresAt int64, ciphertextSHA256 string) Manifest {
	files := make([]ManifestFile, len(plan.Files))
	for i := range plan.Files {
		files[i] = plan.Files[i].ManifestFile
	}
	return Manifest{
		Protocol: Protocol, TransferID: transferID, CreatedAt: createdAt, ExpiresAt: expiresAt,
		CiphertextSHA256: ciphertextSHA256, Files: files, Sender: nil, Signature: nil,
	}
}

type ReceiveOptions struct {
	OutputDir string
	Overwrite bool
	Retries   int
	Progress  func(receivedCiphertextBytes int64) error
}

// PreflightReceive validates destination paths and collisions before a caller
// obtains a server-side claim lease.
func PreflightReceive(manifest Manifest, opts ReceiveOptions) error {
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if opts.OutputDir == "" {
		opts.OutputDir = "."
	}
	for _, mf := range manifest.Files {
		final, err := safeDestination(opts.OutputDir, mf.Path)
		if err != nil {
			return err
		}
		if err := secureMkdirAll(opts.OutputDir, filepath.Dir(final)); err != nil {
			return err
		}
		if _, err := os.Lstat(final); err == nil && !opts.Overwrite {
			return fmt.Errorf("output file already exists: %s", final)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Lstat(final + ".partial"); err == nil {
			return fmt.Errorf("partial output already exists: %s.partial", final)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// ReceiveBundle opens ciphertext at verified chunk boundaries and decrypts to
// sibling .partial files. Open receives the aggregate byte offset and may use
// an HTTP Range request. A body failure is retried without repeating completed
// chunks. Destination names are installed only after every file verifies.
func ReceiveBundle(ctx context.Context, manifest Manifest, master [32]byte, open func(context.Context, int64) (io.ReadCloser, error), opts ReceiveOptions) error {
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if opts.OutputDir == "" {
		opts.OutputDir = "."
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	}
	type target struct {
		final     string
		partial   string
		backup    string
		installed bool
		file      *os.File
		hash      io.Writer
		hasher    interface{ Sum([]byte) []byte }
	}
	targets := make([]target, len(manifest.Files))
	cleanup := func() {
		for i := range targets {
			if targets[i].file != nil {
				_ = targets[i].file.Close()
			}
			if targets[i].partial != "" {
				_ = os.Remove(targets[i].partial)
			}
		}
	}
	for i, mf := range manifest.Files {
		final, err := safeDestination(opts.OutputDir, mf.Path)
		if err != nil {
			cleanup()
			return err
		}
		if _, err := os.Lstat(final); err == nil && !opts.Overwrite {
			cleanup()
			return fmt.Errorf("output file already exists: %s", final)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanup()
			return err
		}
		if err := secureMkdirAll(opts.OutputDir, filepath.Dir(final)); err != nil {
			cleanup()
			return err
		}
		partial := final + ".partial"
		fh, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			cleanup()
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("partial output already exists: %s", partial)
			}
			return err
		}
		h := sha256.New()
		targets[i] = target{final: final, partial: partial, file: fh, hash: io.MultiWriter(fh, h), hasher: h}
	}

	var body io.ReadCloser
	var offset int64
	ciphertextHash := sha256.New()
	openAtOffset := func() error {
		if body != nil {
			_ = body.Close()
		}
		var err error
		body, err = open(ctx, offset)
		return err
	}
	if err := openAtOffset(); err != nil {
		cleanup()
		return err
	}
	defer func() {
		if body != nil {
			_ = body.Close()
		}
	}()
	buf := make([]byte, DefaultChunkSize+gcmTagSize)
	for fileIndex, mf := range manifest.Files {
		for chunkIndex, chunk := range mf.Chunks {
			if err := ctx.Err(); err != nil {
				cleanup()
				return err
			}
			n := chunk.PlaintextBytes + gcmTagSize
			ciphertext := buf[:n]
			var readErr error
			for attempt := 0; ; attempt++ {
				_, readErr = io.ReadFull(body, ciphertext)
				if readErr == nil {
					break
				}
				if attempt >= opts.Retries {
					cleanup()
					return fmt.Errorf("read ciphertext at byte %d: %w", offset, readErr)
				}
				if err := openAtOffset(); err != nil {
					cleanup()
					return err
				}
			}
			plain, err := DecryptChunk(master, manifest.TransferID, fileIndex, chunk.GlobalIndex, chunkIndex, chunk.PlaintextBytes, ciphertext)
			if err != nil {
				cleanup()
				return err
			}
			if _, err := targets[fileIndex].hash.Write(plain); err != nil {
				cleanup()
				return err
			}
			_, _ = ciphertextHash.Write(ciphertext)
			offset += n
			if opts.Progress != nil {
				if err := opts.Progress(offset); err != nil {
					cleanup()
					return err
				}
			}
		}
		if got := hex.EncodeToString(targets[fileIndex].hasher.Sum(nil)); got != mf.SHA256 {
			cleanup()
			return fmt.Errorf("plaintext checksum mismatch for %s", mf.Path)
		}
		if err := targets[fileIndex].file.Sync(); err != nil {
			cleanup()
			return err
		}
		if err := targets[fileIndex].file.Close(); err != nil {
			cleanup()
			return err
		}
		targets[fileIndex].file = nil
	}
	var trailing [1]byte
	if n, err := body.Read(trailing[:]); n != 0 || err != io.EOF {
		cleanup()
		return errors.New("ciphertext has trailing or unreadable data")
	}
	if got := hex.EncodeToString(ciphertextHash.Sum(nil)); got != manifest.CiphertextSHA256 {
		cleanup()
		return errors.New("aggregate ciphertext checksum mismatch")
	}
	rollback := func() {
		for i := range targets {
			if targets[i].installed {
				_ = os.Remove(targets[i].final)
				targets[i].installed = false
			}
		}
		for i := range targets {
			if targets[i].backup != "" {
				_ = os.Rename(targets[i].backup, targets[i].final)
				targets[i].backup = ""
			}
		}
	}
	if opts.Overwrite {
		for i := range targets {
			if _, err := os.Lstat(targets[i].final); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				rollback()
				cleanup()
				return err
			}
			backup, err := reserveBackupPath(targets[i].final)
			if err != nil {
				rollback()
				cleanup()
				return err
			}
			if err := os.Rename(targets[i].final, backup); err != nil {
				rollback()
				cleanup()
				return err
			}
			targets[i].backup = backup
		}
	}
	for i := range targets {
		if err := os.Rename(targets[i].partial, targets[i].final); err != nil {
			rollback()
			cleanup()
			return err
		}
		targets[i].partial = ""
		targets[i].installed = true
	}
	for i := range targets {
		if targets[i].backup != "" {
			_ = os.Remove(targets[i].backup)
			targets[i].backup = ""
		}
	}
	return nil
}

func reserveBackupPath(final string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(final), "."+filepath.Base(final)+".aispace-backup-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	return name, nil
}

func safeDestination(root, slashPath string) (string, error) {
	if err := validatePath(slashPath); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		for _, segment := range strings.Split(slashPath, "/") {
			upper := strings.ToUpper(segment)
			base := strings.TrimSuffix(upper, filepath.Ext(upper))
			reservedPort := len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
			if strings.HasSuffix(segment, " ") || strings.HasSuffix(segment, ".") || strings.Contains(segment, ":") || base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || reservedPort {
				return "", fmt.Errorf("path %q uses a reserved Windows name", slashPath)
			}
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	final := filepath.Join(rootAbs, filepath.FromSlash(slashPath))
	rel, err := filepath.Rel(rootAbs, final)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("sealed manifest path escapes output directory")
	}
	return final, nil
}

func secureMkdirAll(root, dir string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(rootAbs, 0o700); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("output root is not a real directory: %s", rootAbs)
	}
	rel, err := filepath.Rel(rootAbs, dir)
	if err != nil {
		return err
	}
	current := rootAbs
	if rel == "." {
		return nil
	}
	for _, segment := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		st, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return fmt.Errorf("output parent is not a real directory: %s", current)
		}
	}
	return nil
}
