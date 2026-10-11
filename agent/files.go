package agent

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

	"vartio/internal/entities"
)

func stagingDir() string {
	if d := strings.TrimSpace(getenv("VARTIO_AGENT_FILE_STAGING", "GUEST_FILE_STAGING")); d != "" {
		return filepath.Clean(d)
	}
	if runtime.GOOS == "windows" {
		return existingFile(`C:\ProgramData\vartio-agent\tmp`, `C:\ProgramData\probe-agent\tmp`)
	}
	return existingFile(filepath.Join(os.TempDir(), "vartio-agent"), filepath.Join(os.TempDir(), "probe-agent"))
}

func ensureStaging() (string, error) {
	d := stagingDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func resolveGuestPath(p string) (string, *entities.FrameError) {
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
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", asRPC(err)
		}
		return filepath.Clean(filepath.Join(home, p[1:])), nil
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

func filesList(params json.RawMessage) (entities.FilesListResult, *entities.FrameError) {
	var req entities.FilesListRequest
	_ = json.Unmarshal(params, &req)
	abs, ferr := resolveGuestPath(req.Path)
	if ferr != nil {
		return entities.FilesListResult{}, ferr
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return entities.FilesListResult{}, &entities.FrameError{Code: "not_found", Message: abs}
		}
		return entities.FilesListResult{}, asRPC(err)
	}
	out := entities.FilesListResult{
		Path: abs, Staging: stagingDir(), Sep: string(filepath.Separator),
		Roots: fileRoots(), Trash: true, Entries: []entities.FileEntry{},
	}
	out.Home, _ = os.UserHomeDir()
	if parent := filepath.Dir(abs); parent != abs {
		out.Parent = parent
	}
	if !st.IsDir() {
		out.Entries = append(out.Entries, fileEntry(abs, st))
		return out, nil
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return entities.FilesListResult{}, asRPC(err)
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(abs, e.Name())
		fe := fileEntry(full, info)
		if e.Type()&os.ModeSymlink != 0 {
			fe.Link = true
			if st, err := os.Stat(full); err == nil {
				fe.IsDir = st.IsDir()
			}
		}
		out.Entries = append(out.Entries, fe)
	}
	return out, nil
}

func fileEntry(abs string, st os.FileInfo) entities.FileEntry {
	return entities.FileEntry{
		Name:     st.Name(),
		Path:     abs,
		IsDir:    st.IsDir(),
		Size:     st.Size(),
		MtimeUTC: st.ModTime().UTC().Format(time.RFC3339),
		Hidden:   strings.HasPrefix(st.Name(), ".") || hiddenAttr(st),
	}
}

func filesGet(params json.RawMessage) (entities.FilesGetResult, *entities.FrameError) {
	var req entities.FilesGetRequest
	if err := json.Unmarshal(params, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		return entities.FilesGetResult{}, bad("path required")
	}
	abs, ferr := resolveGuestPath(req.Path)
	if ferr != nil {
		return entities.FilesGetResult{}, ferr
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return entities.FilesGetResult{}, &entities.FrameError{Code: "not_found", Message: abs}
		}
		return entities.FilesGetResult{}, asRPC(err)
	}
	out := entities.FilesGetResult{Path: abs, Size: st.Size(), IsDir: st.IsDir(), Offset: req.Offset}
	if req.Stat || st.IsDir() {
		out.EOF = true
		return out, nil
	}
	if st.Size() > entities.FileMaxBytes {
		return entities.FilesGetResult{}, &entities.FrameError{Code: "too_large", Message: "file exceeds 512MB"}
	}
	length := req.Length
	if length <= 0 {
		length = entities.FileChunk
	}
	f, err := os.Open(abs)
	if err != nil {
		return entities.FilesGetResult{}, asRPC(err)
	}
	defer f.Close()
	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			return entities.FilesGetResult{}, asRPC(err)
		}
	}
	buf := make([]byte, length)
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	if err != nil {
		return entities.FilesGetResult{}, asRPC(err)
	}
	out.Data = buf[:n]
	out.EOF = req.Offset+int64(n) >= st.Size()
	if out.EOF {
		sum, herr := hashFile(abs)
		if herr != nil {
			return entities.FilesGetResult{}, asRPC(herr)
		}
		out.SHA256 = sum
	}
	return out, nil
}

func filesPut(params json.RawMessage) (entities.FilesPutResult, *entities.FrameError) {
	var req entities.FilesPutRequest
	if err := json.Unmarshal(params, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		return entities.FilesPutResult{}, bad("path required")
	}
	abs, ferr := resolveGuestPath(req.Path)
	if ferr != nil {
		return entities.FilesPutResult{}, ferr
	}
	if req.Offset+int64(len(req.Data)) > entities.FileMaxBytes {
		return entities.FilesPutResult{}, &entities.FrameError{Code: "too_large", Message: "upload exceeds 512MB"}
	}
	part := abs + ".part"
	if req.Offset == 0 {
		if st, err := os.Stat(abs); err == nil {
			if st.IsDir() {
				return entities.FilesPutResult{}, bad("path is a directory")
			}
			if !req.Confirm {
				return entities.FilesPutResult{}, &entities.FrameError{Code: "confirm_required", Message: "overwrite requires confirm"}
			}
		} else if !os.IsNotExist(err) {
			return entities.FilesPutResult{}, asRPC(err)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return entities.FilesPutResult{}, asRPC(err)
		}
		if err := os.WriteFile(part, req.Data, 0o644); err != nil {
			return entities.FilesPutResult{}, asRPC(err)
		}
	} else {
		f, err := os.OpenFile(part, os.O_WRONLY, 0o600)
		if err != nil {
			return entities.FilesPutResult{}, asRPC(err)
		}
		_, err = f.WriteAt(req.Data, req.Offset)
		_ = f.Close()
		if err != nil {
			return entities.FilesPutResult{}, asRPC(err)
		}
	}
	st, err := os.Stat(part)
	if err != nil {
		return entities.FilesPutResult{}, asRPC(err)
	}
	out := entities.FilesPutResult{OK: true, Path: abs, Offset: req.Offset + int64(len(req.Data)), Size: st.Size()}
	if !req.EOF {
		return out, nil
	}
	sum, err := hashFile(part)
	if err != nil {
		return entities.FilesPutResult{}, asRPC(err)
	}
	if req.SHA256 != "" && !strings.EqualFold(req.SHA256, sum) {
		_ = os.Remove(part)
		return entities.FilesPutResult{}, bad("sha256 mismatch")
	}
	if err := os.Rename(part, abs); err != nil {
		return entities.FilesPutResult{}, asRPC(err)
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
