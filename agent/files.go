package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"govmomi-wmks/internal/guest"
)

func stagingDir() string {
	if d := strings.TrimSpace(os.Getenv("GUEST_FILE_STAGING")); d != "" {
		return filepath.Clean(d)
	}
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\probe-agent\tmp`
	}
	return filepath.Join(os.TempDir(), "probe-agent")
}

func ensureStaging() (string, error) {
	d := stagingDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func resolveGuestPath(p string) (string, *guest.FrameError) {
	p = strings.TrimSpace(p)
	if strings.ContainsRune(p, 0) {
		return "", bad("invalid path")
	}
	stage, err := ensureStaging()
	if err != nil {
		return "", asRPC(err)
	}
	if p == "" || p == "." {
		return stage, nil
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	abs := filepath.Clean(filepath.Join(stage, p))
	rel, err := filepath.Rel(stage, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", bad("path escapes staging")
	}
	return abs, nil
}

func filesList(params json.RawMessage) (guest.FilesListResult, *guest.FrameError) {
	var req guest.FilesListRequest
	_ = json.Unmarshal(params, &req)
	abs, ferr := resolveGuestPath(req.Path)
	if ferr != nil {
		return guest.FilesListResult{}, ferr
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return guest.FilesListResult{}, &guest.FrameError{Code: "not_found", Message: abs}
		}
		return guest.FilesListResult{}, asRPC(err)
	}
	out := guest.FilesListResult{Path: abs, Staging: stagingDir(), Entries: []guest.FileEntry{}}
	if !st.IsDir() {
		out.Entries = append(out.Entries, fileEntry(abs, st))
		return out, nil
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return guest.FilesListResult{}, asRPC(err)
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, fileEntry(filepath.Join(abs, e.Name()), info))
	}
	return out, nil
}

func fileEntry(abs string, st os.FileInfo) guest.FileEntry {
	return guest.FileEntry{
		Name:     st.Name(),
		Path:     abs,
		IsDir:    st.IsDir(),
		Size:     st.Size(),
		MtimeUTC: st.ModTime().UTC().Format(time.RFC3339),
	}
}

func filesGet(params json.RawMessage) (guest.FilesGetResult, *guest.FrameError) {
	var req guest.FilesGetRequest
	if err := json.Unmarshal(params, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		return guest.FilesGetResult{}, bad("path required")
	}
	abs, ferr := resolveGuestPath(req.Path)
	if ferr != nil {
		return guest.FilesGetResult{}, ferr
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return guest.FilesGetResult{}, &guest.FrameError{Code: "not_found", Message: abs}
		}
		return guest.FilesGetResult{}, asRPC(err)
	}
	out := guest.FilesGetResult{Path: abs, Size: st.Size(), IsDir: st.IsDir(), Offset: req.Offset}
	if req.Stat || st.IsDir() {
		out.EOF = true
		return out, nil
	}
	if st.Size() > guest.FileMaxBytes {
		return guest.FilesGetResult{}, &guest.FrameError{Code: "too_large", Message: "file exceeds 512MB"}
	}
	length := req.Length
	if length <= 0 {
		length = guest.FileChunk
	}
	f, err := os.Open(abs)
	if err != nil {
		return guest.FilesGetResult{}, asRPC(err)
	}
	defer f.Close()
	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			return guest.FilesGetResult{}, asRPC(err)
		}
	}
	buf := make([]byte, length)
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	if err != nil {
		return guest.FilesGetResult{}, asRPC(err)
	}
	out.Data = buf[:n]
	out.EOF = req.Offset+int64(n) >= st.Size()
	if out.EOF {
		sum, herr := hashFile(abs)
		if herr != nil {
			return guest.FilesGetResult{}, asRPC(herr)
		}
		out.SHA256 = sum
	}
	return out, nil
}

func filesPut(params json.RawMessage) (guest.FilesPutResult, *guest.FrameError) {
	var req guest.FilesPutRequest
	if err := json.Unmarshal(params, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		return guest.FilesPutResult{}, bad("path required")
	}
	abs, ferr := resolveGuestPath(req.Path)
	if ferr != nil {
		return guest.FilesPutResult{}, ferr
	}
	if req.Offset+int64(len(req.Data)) > guest.FileMaxBytes {
		return guest.FilesPutResult{}, &guest.FrameError{Code: "too_large", Message: "upload exceeds 512MB"}
	}
	part := abs + ".part"
	if req.Offset == 0 {
		if st, err := os.Stat(abs); err == nil {
			if st.IsDir() {
				return guest.FilesPutResult{}, bad("path is a directory")
			}
			if !req.Confirm {
				return guest.FilesPutResult{}, &guest.FrameError{Code: "confirm_required", Message: "overwrite requires confirm"}
			}
		} else if !os.IsNotExist(err) {
			return guest.FilesPutResult{}, asRPC(err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return guest.FilesPutResult{}, asRPC(err)
		}
		if err := os.WriteFile(part, req.Data, 0o600); err != nil {
			return guest.FilesPutResult{}, asRPC(err)
		}
	} else {
		f, err := os.OpenFile(part, os.O_WRONLY, 0o600)
		if err != nil {
			return guest.FilesPutResult{}, asRPC(err)
		}
		_, err = f.WriteAt(req.Data, req.Offset)
		_ = f.Close()
		if err != nil {
			return guest.FilesPutResult{}, asRPC(err)
		}
	}
	st, err := os.Stat(part)
	if err != nil {
		return guest.FilesPutResult{}, asRPC(err)
	}
	out := guest.FilesPutResult{OK: true, Path: abs, Offset: req.Offset + int64(len(req.Data)), Size: st.Size()}
	if !req.EOF {
		return out, nil
	}
	sum, err := hashFile(part)
	if err != nil {
		return guest.FilesPutResult{}, asRPC(err)
	}
	if req.SHA256 != "" && !strings.EqualFold(req.SHA256, sum) {
		_ = os.Remove(part)
		return guest.FilesPutResult{}, bad("sha256 mismatch")
	}
	if err := os.Rename(part, abs); err != nil {
		return guest.FilesPutResult{}, asRPC(err)
	}
	out.SHA256 = sum
	out.Size = st.Size()
	return out, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
