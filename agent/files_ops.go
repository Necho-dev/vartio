package agent

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vartio/internal/entities"
)

func filesOp(params json.RawMessage) (entities.FilesOpResult, *entities.FrameError) {
	var req entities.FilesOpRequest
	if err := json.Unmarshal(params, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		return entities.FilesOpResult{}, bad("op and path required")
	}
	abs, ferr := resolveGuestPath(req.Path)
	if ferr != nil {
		return entities.FilesOpResult{}, ferr
	}
	switch req.Op {
	case entities.FileOpMkdir:
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return entities.FilesOpResult{}, asRPC(err)
		}
		return entities.FilesOpResult{OK: true, Path: abs}, nil
	case entities.FileOpRename:
		return fileRename(abs, req.To)
	case entities.FileOpDelete:
		return fileDelete(abs, req.Confirm)
	case entities.FileOpTrash:
		if protectedPath(abs) {
			return entities.FilesOpResult{}, bad("refusing to delete a root or home directory")
		}
		if _, err := os.Lstat(abs); err != nil {
			return entities.FilesOpResult{}, notFoundOr(err, abs)
		}
		if ferr := moveToTrash(abs); ferr != nil {
			return entities.FilesOpResult{}, ferr
		}
		return entities.FilesOpResult{OK: true, Path: abs, Count: 1}, nil
	case entities.FileOpZip:
		return fileZip(abs)
	default:
		return entities.FilesOpResult{}, bad("unknown op " + req.Op)
	}
}

// protectedPath refuses filesystem roots and the user's home directory.
func protectedPath(abs string) bool {
	if filepath.Dir(abs) == abs {
		return true
	}
	home, _ := os.UserHomeDir()
	return home != "" && strings.EqualFold(filepath.Clean(home), abs)
}

func fileRename(abs, to string) (entities.FilesOpResult, *entities.FrameError) {
	to = strings.TrimSpace(to)
	if to == "" || strings.ContainsRune(to, 0) {
		return entities.FilesOpResult{}, bad("to required")
	}
	if protectedPath(abs) {
		return entities.FilesOpResult{}, bad("refusing to move a root or home directory")
	}
	dst := to
	if !strings.ContainsAny(to, `/\`) {
		dst = filepath.Join(filepath.Dir(abs), to)
	} else {
		var ferr *entities.FrameError
		if dst, ferr = resolveGuestPath(to); ferr != nil {
			return entities.FilesOpResult{}, ferr
		}
	}
	if _, err := os.Lstat(abs); err != nil {
		return entities.FilesOpResult{}, notFoundOr(err, abs)
	}
	// Case-only renames on case-insensitive filesystems hit the same file.
	if _, err := os.Lstat(dst); err == nil && !strings.EqualFold(abs, dst) {
		return entities.FilesOpResult{}, &entities.FrameError{Code: "conflict", Message: dst + " already exists"}
	}
	if err := os.Rename(abs, dst); err != nil {
		return entities.FilesOpResult{}, asRPC(err)
	}
	return entities.FilesOpResult{OK: true, Path: dst}, nil
}

func fileDelete(abs string, confirm bool) (entities.FilesOpResult, *entities.FrameError) {
	if protectedPath(abs) {
		return entities.FilesOpResult{}, bad("refusing to delete a root or home directory")
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return entities.FilesOpResult{}, notFoundOr(err, abs)
	}
	if !st.IsDir() {
		if err := os.Remove(abs); err != nil {
			return entities.FilesOpResult{}, asRPC(err)
		}
		return entities.FilesOpResult{OK: true, Path: abs, Count: 1}, nil
	}
	if err := os.Remove(abs); err == nil {
		return entities.FilesOpResult{OK: true, Path: abs, Count: 1}, nil
	}
	if !confirm {
		return entities.FilesOpResult{}, &entities.FrameError{Code: "confirm_required", Message: "directory is not empty"}
	}
	n := 0
	_ = filepath.WalkDir(abs, func(string, fs.DirEntry, error) error { n++; return nil })
	if err := os.RemoveAll(abs); err != nil {
		return entities.FilesOpResult{}, asRPC(err)
	}
	return entities.FilesOpResult{OK: true, Path: abs, Count: n}, nil
}

// fileZip archives a directory into <staging>/zip-<ts>/<name>.zip; the caller
// downloads it and deletes the zip-<ts> directory.
func fileZip(abs string) (entities.FilesOpResult, *entities.FrameError) {
	st, err := os.Stat(abs)
	if err != nil {
		return entities.FilesOpResult{}, notFoundOr(err, abs)
	}
	if !st.IsDir() {
		return entities.FilesOpResult{}, bad("zip expects a directory")
	}
	stage, err := ensureStaging()
	if err != nil {
		return entities.FilesOpResult{}, asRPC(err)
	}
	dir := filepath.Join(stage, fmt.Sprintf("zip-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return entities.FilesOpResult{}, asRPC(err)
	}
	name := filepath.Base(abs)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "archive"
	}
	out := filepath.Join(dir, strings.TrimSuffix(name, `:`)+".zip")
	n, err := writeZip(out, abs)
	if err != nil {
		_ = os.RemoveAll(dir)
		var fe *entities.FrameError
		if errors.As(err, &fe) {
			return entities.FilesOpResult{}, fe
		}
		return entities.FilesOpResult{}, asRPC(err)
	}
	zst, err := os.Stat(out)
	if err != nil {
		return entities.FilesOpResult{}, asRPC(err)
	}
	return entities.FilesOpResult{OK: true, Path: out, Size: zst.Size(), Count: n}, nil
}

func writeZip(out, root string) (int, error) {
	f, err := os.Create(out)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	var total int64
	n := 0
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		name := filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/", Modified: info.ModTime()})
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if total += info.Size(); total > entities.FileMaxBytes {
			return &entities.FrameError{Code: "too_large", Message: "directory exceeds 512MB"}
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Method = name, zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return nil
		}
		_, err = io.Copy(w, src)
		_ = src.Close()
		if err == nil {
			n++
		}
		return err
	})
	if walkErr != nil {
		_ = zw.Close()
		return 0, walkErr
	}
	return n, zw.Close()
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// uniqueName returns base, or "stem 2.ext", "stem 3.ext"… if taken.
func uniqueName(base string, taken func(string) bool) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem, ext = base, ""
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s %d%s", stem, i, ext)
	}
	return name
}

func notFoundOr(err error, abs string) *entities.FrameError {
	if os.IsNotExist(err) {
		return &entities.FrameError{Code: "not_found", Message: abs}
	}
	return asRPC(err)
}
