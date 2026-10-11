//go:build !windows

package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"vartio/internal/entities"
)

const trashInfoTime = "2006-01-02T15:04:05"

// trashDirs returns the directory holding trashed items and, on freedesktop
// systems, the matching info directory ("" on macOS).
func trashDirs() (files, info string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, ".Trash"), "", nil
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "Trash", "files"), filepath.Join(data, "Trash", "info"), nil
}

// moveToTrash moves abs into the agent user's Trash: ~/.Trash on macOS, the
// freedesktop.org home trash elsewhere. Items on another filesystem are
// refused rather than copied.
func moveToTrash(abs string) *entities.FrameError {
	files, info, err := trashDirs()
	if err != nil {
		return asRPC(err)
	}
	for _, d := range []string{files, info} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return asRPC(err)
		}
	}
	name := uniqueName(filepath.Base(abs), func(n string) bool {
		return exists(filepath.Join(files, n)) || (info != "" && exists(filepath.Join(info, n+".trashinfo")))
	})
	if info == "" {
		if ferr := trashRename(abs, filepath.Join(files, name)); ferr != nil {
			return ferr
		}
		macIndexPut(name, abs)
		return nil
	}
	infoPath := filepath.Join(info, name+".trashinfo")
	f, err := os.OpenFile(infoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return asRPC(err)
	}
	_, err = fmt.Fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		(&url.URL{Path: abs}).EscapedPath(), time.Now().Format(trashInfoTime))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(infoPath)
		return asRPC(err)
	}
	if ferr := trashRename(abs, filepath.Join(files, name)); ferr != nil {
		_ = os.Remove(infoPath)
		return ferr
	}
	return nil
}

func trashRename(src, dst string) *entities.FrameError {
	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return &entities.FrameError{Code: "not_supported", Message: "not on the same disk as the Trash"}
		}
		return asRPC(err)
	}
	return nil
}

func trashItems() ([]entities.TrashItem, string, *entities.FrameError) {
	files, info, err := trashDirs()
	if err != nil {
		return nil, "", asRPC(err)
	}
	items := []entities.TrashItem{}
	ents, err := os.ReadDir(files)
	if err != nil && !os.IsNotExist(err) {
		if os.IsPermission(err) {
			return nil, files, &entities.FrameError{Code: "not_supported", Message: "no permission to read " + files + " (macOS needs Full Disk Access for the agent)"}
		}
		return nil, files, asRPC(err)
	}
	var idx map[string]macTrashRecord
	if info == "" {
		idx = macIndexLoad()
	}
	seen := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		if name == ".DS_Store" || name == ".localized" {
			continue
		}
		seen[name] = true
		var orig, deleted string
		if info == "" {
			if r, ok := idx[name]; ok {
				orig, deleted = r.Orig, r.Deleted
			}
		} else {
			orig, deleted = readTrashInfo(filepath.Join(info, name+".trashinfo"))
		}
		display := name
		if orig != "" {
			display = filepath.Base(orig)
		}
		if it, ok := trashEntry(filepath.Join(files, name), display, orig, deleted); ok {
			items = append(items, it)
		}
	}
	if info == "" {
		macIndexPrune(seen)
	}
	return items, files, nil
}

func trashOrig(id string) string {
	_, info, _ := trashDirs()
	name := filepath.Base(id)
	if info == "" {
		return macIndexLoad()[name].Orig
	}
	orig, _ := readTrashInfo(filepath.Join(info, name+".trashinfo"))
	return orig
}

func trashForget(id string) {
	_, info, _ := trashDirs()
	name := filepath.Base(id)
	if info == "" {
		macIndexDel(name)
		return
	}
	_ = os.Remove(filepath.Join(info, name+".trashinfo"))
}

func readTrashInfo(p string) (orig, deletedUtc string) {
	f, err := os.Open(p)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "Path":
			if u, err := url.PathUnescape(v); err == nil {
				orig = u
			}
		case "DeletionDate":
			if t, err := time.ParseInLocation(trashInfoTime, v, time.Local); err == nil {
				deletedUtc = t.UTC().Format(time.RFC3339)
			}
		}
	}
	if sc.Err() != nil {
		return "", ""
	}
	return orig, deletedUtc
}

// macOS keeps "put back" locations in Finder's private .DS_Store, so the agent
// records its own: trash name -> original path, in the user config dir.
type macTrashRecord struct {
	Orig    string `json:"orig"`
	Deleted string `json:"deleted"`
}

var macIndexMu sync.Mutex

func macIndexPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return existingFile(filepath.Join(dir, "vartio", "trash-index.json"), filepath.Join(dir, "probe-agent", "trash-index.json"))
}

func macIndexLoad() map[string]macTrashRecord {
	macIndexMu.Lock()
	defer macIndexMu.Unlock()
	return macIndexRead()
}

func macIndexRead() map[string]macTrashRecord {
	m := map[string]macTrashRecord{}
	if b, err := os.ReadFile(macIndexPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func macIndexUpdate(fn func(map[string]macTrashRecord) bool) {
	p := macIndexPath()
	if p == "" {
		return
	}
	macIndexMu.Lock()
	defer macIndexMu.Unlock()
	m := macIndexRead()
	if !fn(m) {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if b, err := json.Marshal(m); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

func macIndexPut(name, orig string) {
	macIndexUpdate(func(m map[string]macTrashRecord) bool {
		m[name] = macTrashRecord{Orig: orig, Deleted: time.Now().UTC().Format(time.RFC3339)}
		return true
	})
}

func macIndexDel(name string) {
	macIndexUpdate(func(m map[string]macTrashRecord) bool {
		_, ok := m[name]
		delete(m, name)
		return ok
	})
}

func macIndexPrune(seen map[string]bool) {
	macIndexUpdate(func(m map[string]macTrashRecord) bool {
		changed := false
		for k := range m {
			if !seen[k] {
				delete(m, k)
				changed = true
			}
		}
		return changed
	})
}
