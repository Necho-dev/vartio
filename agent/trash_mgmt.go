package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"vartio/internal/entities"
)

// Platform hooks (trash_unix.go / trash_windows.go):
//   trashItems() lists the agent user's trash;
//   trashOrig(id) reports where an item came from ("" if unknown);
//   trashForget(id) drops the item's metadata after restore/purge.

func trashOp(params json.RawMessage) (entities.TrashResult, *entities.FrameError) {
	var req entities.TrashRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return entities.TrashResult{}, bad("invalid params")
	}
	items, loc, ferr := trashItems()
	if ferr != nil {
		return entities.TrashResult{}, ferr
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeletedUtc > items[j].DeletedUtc })
	out := entities.TrashResult{Location: loc, Items: items}
	known := make(map[string]bool, len(items))
	for _, it := range items {
		known[it.ID] = true
	}
	ids := req.IDs
	switch req.Op {
	case entities.TrashList:
		return out, nil
	case entities.TrashEmpty:
		ids = ids[:0]
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		fallthrough
	case entities.TrashPurge:
		if !req.Confirm {
			return entities.TrashResult{}, &entities.FrameError{Code: "confirm_required", Message: "purge needs confirm"}
		}
	case entities.TrashRestore:
	default:
		return entities.TrashResult{}, bad("unknown op " + req.Op)
	}
	for _, id := range ids {
		if !known[id] {
			out.Failed = append(out.Failed, entities.TrashFailure{ID: id, Message: "not in trash"})
			continue
		}
		var err error
		if req.Op == entities.TrashRestore {
			var dst string
			if dst, err = trashRestore(id); err == nil {
				out.Paths = append(out.Paths, dst)
			}
		} else if err = os.RemoveAll(id); err == nil {
			trashForget(id)
		}
		if err != nil {
			out.Failed = append(out.Failed, entities.TrashFailure{ID: id, Message: err.Error()})
			continue
		}
		out.Done++
	}
	out.Items, _, _ = trashItems()
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].DeletedUtc > out.Items[j].DeletedUtc })
	return out, nil
}

// trashRestore moves an item back to its original folder, or to the home
// directory when the origin is unknown; an existing name gets " 2", " 3"….
func trashRestore(id string) (string, error) {
	orig := trashOrig(id)
	if orig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		orig = filepath.Join(home, filepath.Base(id))
	}
	dir := filepath.Dir(orig)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, uniqueName(filepath.Base(orig), func(n string) bool { return exists(filepath.Join(dir, n)) }))
	if err := os.Rename(id, dst); err != nil {
		return "", err
	}
	trashForget(id)
	return dst, nil
}

func trashEntry(id, name, orig string, deleted string) (entities.TrashItem, bool) {
	st, err := os.Lstat(id)
	if err != nil {
		return entities.TrashItem{}, false
	}
	it := entities.TrashItem{ID: id, Name: name, Orig: orig, IsDir: st.IsDir(), DeletedUtc: deleted}
	if !st.IsDir() {
		it.Size = st.Size()
	}
	return it, true
}
