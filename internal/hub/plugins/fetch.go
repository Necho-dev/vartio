package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	sdk "github.com/Necho-dev/vartio/sdk"
	"vartio/internal/common"
)

const maxPackage = 256 << 20

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// downloadPackage fetches a .vtp. size 0 skips the exact-size check; the body
// still cannot exceed maxPackage.
func downloadPackage(ctx context.Context, raw string, size int64) ([]byte, error) {
	u, err := validateFetchURL(raw)
	if err != nil {
		return nil, err
	}
	if size < 0 || size > maxPackage {
		return nil, common.ErrBad("安装包大小不合法")
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, common.ErrBad("下载安装包失败: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, common.ErrBad("安装包返回 HTTP %d", res.StatusCode)
	}
	limit := int64(maxPackage)
	if size > 0 {
		limit = size
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, common.ErrBad("安装包超过声明的大小")
	}
	if size > 0 && int64(len(body)) != size {
		return nil, common.ErrBad("安装包大小不符")
	}
	return body, nil
}

func packageSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writePackage(dir string, pkg sdk.Package) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	staging := dir + ".tmp"
	_ = os.RemoveAll(staging)
	if err := pkg.WriteTree(staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(staging, dir); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	return nil
}

func readTree(dir string) (sdk.Package, error) {
	man, err := os.ReadFile(filepath.Join(dir, sdk.ManifestName))
	if err != nil {
		return sdk.Package{}, err
	}
	bin, err := os.ReadFile(filepath.Join(dir, sdk.BinaryName))
	if err != nil {
		return sdk.Package{}, err
	}
	sums, err := os.ReadFile(filepath.Join(dir, sdk.ChecksumName))
	if err != nil {
		return sdk.Package{}, err
	}
	sigRaw, err := os.ReadFile(filepath.Join(dir, sdk.SignatureName))
	if err != nil {
		return sdk.Package{}, err
	}
	var sig sdk.Signature
	if err := json.Unmarshal(sigRaw, &sig); err != nil {
		return sdk.Package{}, err
	}
	return sdk.Package{ManifestRaw: man, Binary: bin, Checksums: sums, Signature: sig}, nil
}

func localOK(dir string, pub ed25519.PublicKey, keyID string, want sdk.Manifest) bool {
	pkg, err := readTree(dir)
	if err != nil {
		return false
	}
	got, err := pkg.Verify(pub, keyID)
	return err == nil && sdk.SameManifest(got, want)
}

func (r *Registry) keepOnly(name, version string) {
	root := filepath.Join(r.Dir, name)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == version {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}
