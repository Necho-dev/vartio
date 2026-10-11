package plugins

import (
	"crypto/ed25519"

	sdk "github.com/Necho-dev/vartio/sdk"
	"vartio/internal/common"
)

const (
	officialKeyID     = "vartio-plugins-2026"
	officialPublicKey = "/NU6/xmJR4N0Nas6OxXBzkVGdn45HZL5h8otwk0xI9w="
)

func (r *Registry) trust(id string, pub ed25519.PublicKey) error {
	if id == "" || len(pub) != ed25519.PublicKeySize {
		return common.ErrBad("公钥不完整")
	}
	if old, ok := r.keys[id]; ok && !edEqual(old, pub) {
		return common.ErrBad("签名密钥 %s 与已信任的公钥不一致", id)
	}
	r.keys[id] = append([]byte(nil), pub...)
	return nil
}

func (r *Registry) forgetKey(id string) {
	if id == "" || id == officialKeyID {
		return
	}
	for _, repo := range r.repos {
		if repo.KeyID == id {
			return
		}
	}
	for _, m := range r.manual {
		if m.SigningKeyID == id {
			return
		}
	}
	delete(r.keys, id)
}

func edEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func officialPublic() ed25519.PublicKey {
	pub, err := sdk.ParsePublicKey(officialPublicKey)
	if err != nil {
		panic(err)
	}
	return pub
}
