package guest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"path/filepath"
	"strconv"

	"govmomi-wmks/internal/esxi"
)

func (h *Hub) CollectFile(ctx context.Context, bios, path string, max int64) ([]byte, FilesGetResult, error) {
	st, err := h.FileStat(ctx, bios, path)
	if err != nil {
		return nil, FilesGetResult{}, err
	}
	if st.IsDir {
		return nil, st, esxi.ErrBad("path is a directory")
	}
	if max > 0 && st.Size > max {
		return nil, st, nil
	}
	if st.Size > FileMaxBytes {
		return nil, st, esxi.ErrTooLarge("file exceeds %d bytes", FileMaxBytes)
	}
	var buf []byte
	var offset int64
	for offset < st.Size {
		raw, err := h.Call(ctx, bios, MethodFilesGet, FilesGetRequest{Path: path, Offset: offset, Length: FileChunk})
		if err != nil {
			return nil, st, err
		}
		chunk, err := decode[FilesGetResult](raw)
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

func (h *Hub) WriteFile(w http.ResponseWriter, ctx context.Context, bios, path string) error {
	if path == "" {
		return esxi.ErrBad("path required")
	}
	st, err := h.FileStat(ctx, bios, path)
	if err != nil {
		return err
	}
	if st.IsDir {
		return esxi.ErrBad("path is a directory")
	}
	if st.Size > FileMaxBytes {
		return esxi.ErrTooLarge("file exceeds %d bytes", FileMaxBytes)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filepath.Base(st.Path)))
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size, 10))
	w.WriteHeader(http.StatusOK)

	var offset int64
	var lastSHA string
	for offset < st.Size {
		raw, err := h.Call(ctx, bios, MethodFilesGet, FilesGetRequest{Path: path, Offset: offset, Length: FileChunk})
		if err != nil {
			return err
		}
		chunk, err := decode[FilesGetResult](raw)
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

func (h *Hub) ReadFile(ctx context.Context, bios, path string, r io.Reader, size int64, shaHex string, confirm bool) (FilesPutResult, error) {
	if path == "" {
		return FilesPutResult{}, esxi.ErrBad("path required")
	}
	if size > FileMaxBytes {
		return FilesPutResult{}, esxi.ErrTooLarge("upload exceeds %d bytes", FileMaxBytes)
	}
	sum := sha256.New()
	var offset int64
	buf := make([]byte, FileChunk)
	var last FilesPutResult
	for {
		n, err := io.ReadFull(r, buf)
		if n == 0 && err == io.EOF {
			raw, err := h.Call(ctx, bios, MethodFilesPut, FilesPutRequest{
				Path: path, Offset: offset, EOF: true, SHA256: shaHex, Confirm: confirm && offset == 0,
			})
			if err != nil {
				return FilesPutResult{}, err
			}
			return decode[FilesPutResult](raw)
		}
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return FilesPutResult{}, err
		}
		chunk := buf[:n]
		sum.Write(chunk)
		eof := err == io.EOF || err == io.ErrUnexpectedEOF
		req := FilesPutRequest{
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
		raw, callErr := h.Call(ctx, bios, MethodFilesPut, req)
		if callErr != nil {
			return FilesPutResult{}, callErr
		}
		last, err = decode[FilesPutResult](raw)
		if err != nil {
			return FilesPutResult{}, err
		}
		offset += int64(n)
		if eof {
			return last, nil
		}
	}
}
