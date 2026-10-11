package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"vartio/internal/common"
	"vartio/internal/entities"
)

func (h *Hub) CollectFile(ctx context.Context, bios, path string, max int64) ([]byte, entities.FilesGetResult, error) {
	st, err := h.FileStat(ctx, bios, path)
	if err != nil {
		return nil, entities.FilesGetResult{}, err
	}
	if st.IsDir {
		return nil, st, common.ErrBad("path is a directory")
	}
	if max > 0 && st.Size > max {
		return nil, st, nil
	}
	if st.Size > entities.FileMaxBytes {
		return nil, st, common.ErrTooLarge("file exceeds %d bytes", entities.FileMaxBytes)
	}
	var buf []byte
	var offset int64
	for offset < st.Size {
		raw, err := h.Call(ctx, bios, entities.MethodFilesGet, entities.FilesGetRequest{Path: path, Offset: offset, Length: entities.FileChunk})
		if err != nil {
			return nil, st, err
		}
		chunk, err := decode[entities.FilesGetResult](raw)
		if err != nil {
			return nil, st, err
		}
		buf = append(buf, chunk.Data...)
		offset += int64(len(chunk.Data))
		st.SHA256 = chunk.SHA256
		if chunk.EOF || len(chunk.Data) == 0 {
			break
		}
	}
	st.EOF = true
	return buf, st, nil
}

func (h *Hub) FilesOp(ctx context.Context, bios string, req entities.FilesOpRequest) (entities.FilesOpResult, error) {
	switch req.Op {
	case entities.FileOpMkdir, entities.FileOpRename, entities.FileOpDelete, entities.FileOpTrash, entities.FileOpZip:
	default:
		return entities.FilesOpResult{}, common.ErrBad("op must be mkdir, rename, delete, trash, or zip")
	}
	if strings.TrimSpace(req.Path) == "" {
		return entities.FilesOpResult{}, common.ErrBad("path required")
	}
	raw, err := h.Call(ctx, bios, entities.MethodFilesOp, req)
	if err != nil {
		return entities.FilesOpResult{}, err
	}
	return decode[entities.FilesOpResult](raw)
}

func (h *Hub) Trash(ctx context.Context, bios string, req entities.TrashRequest) (entities.TrashResult, error) {
	switch req.Op {
	case entities.TrashList, entities.TrashEmpty:
	case entities.TrashRestore, entities.TrashPurge:
		if len(req.IDs) == 0 {
			return entities.TrashResult{}, common.ErrBad("ids required")
		}
	default:
		return entities.TrashResult{}, common.ErrBad("op must be list, restore, purge, or empty")
	}
	raw, err := h.Call(ctx, bios, entities.MethodTrash, req)
	if err != nil {
		return entities.TrashResult{}, err
	}
	return decode[entities.TrashResult](raw)
}

// WriteFile streams a guest file; a directory is zipped on the guest first and
// the temporary archive is removed afterwards.
func (h *Hub) WriteFile(w http.ResponseWriter, ctx context.Context, bios, path string) error {
	if path == "" {
		return common.ErrBad("path required")
	}
	st, err := h.FileStat(ctx, bios, path)
	if err != nil {
		return err
	}
	if st.IsDir {
		z, err := h.FilesOp(ctx, bios, entities.FilesOpRequest{Op: entities.FileOpZip, Path: st.Path})
		if err != nil {
			return err
		}
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), entities.ActionTimeout)
			defer cancel()
			_, _ = h.FilesOp(c, bios, entities.FilesOpRequest{Op: entities.FileOpDelete, Path: filepath.Dir(z.Path), Confirm: true})
		}()
		return h.WriteFile(w, ctx, bios, z.Path)
	}
	if st.Size > entities.FileMaxBytes {
		return common.ErrTooLarge("file exceeds %d bytes", entities.FileMaxBytes)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", contentDisposition(baseName(st.Path)))
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size, 10))
	w.WriteHeader(http.StatusOK)

	var offset int64
	var lastSHA string
	for offset < st.Size {
		raw, err := h.Call(ctx, bios, entities.MethodFilesGet, entities.FilesGetRequest{Path: path, Offset: offset, Length: entities.FileChunk})
		if err != nil {
			return err
		}
		chunk, err := decode[entities.FilesGetResult](raw)
		if err != nil {
			return err
		}
		if len(chunk.Data) == 0 {
			break
		}
		if _, err := w.Write(chunk.Data); err != nil {
			return err
		}
		offset += int64(len(chunk.Data))
		lastSHA = chunk.SHA256
		if chunk.EOF {
			break
		}
	}
	if lastSHA != "" {
		// header already sent; expose via trailer is optional
		_ = lastSHA
	}
	return nil
}

func (h *Hub) ReadFile(ctx context.Context, bios, path string, r io.Reader, size int64, shaHex string, confirm bool) (entities.FilesPutResult, error) {
	if path == "" {
		return entities.FilesPutResult{}, common.ErrBad("path required")
	}
	if size > entities.FileMaxBytes {
		return entities.FilesPutResult{}, common.ErrTooLarge("upload exceeds %d bytes", entities.FileMaxBytes)
	}
	sum := sha256.New()
	var offset int64
	buf := make([]byte, entities.FileChunk)
	var last entities.FilesPutResult
	for {
		n, err := io.ReadFull(r, buf)
		if n == 0 && err == io.EOF {
			raw, err := h.Call(ctx, bios, entities.MethodFilesPut, entities.FilesPutRequest{
				Path: path, Offset: offset, EOF: true, SHA256: shaHex, Confirm: confirm && offset == 0,
			})
			if err != nil {
				return entities.FilesPutResult{}, err
			}
			return decode[entities.FilesPutResult](raw)
		}
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return entities.FilesPutResult{}, err
		}
		chunk := buf[:n]
		sum.Write(chunk)
		eof := err == io.EOF || err == io.ErrUnexpectedEOF
		req := entities.FilesPutRequest{
			Path:    path,
			Offset:  offset,
			Data:    chunk,
			EOF:     eof,
			Confirm: confirm && offset == 0,
		}
		if eof {
			if shaHex == "" {
				shaHex = hex.EncodeToString(sum.Sum(nil))
			}
			req.SHA256 = shaHex
		}
		raw, callErr := h.Call(ctx, bios, entities.MethodFilesPut, req)
		if callErr != nil {
			return entities.FilesPutResult{}, callErr
		}
		last, err = decode[entities.FilesPutResult](raw)
		if err != nil {
			return entities.FilesPutResult{}, err
		}
		offset += int64(n)
		if eof {
			return last, nil
		}
	}
}

// baseName handles both separators: the hub may run on a different OS than the guest.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}
