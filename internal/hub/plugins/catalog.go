package plugins

import (
	_ "embed"
	"regexp"
	"runtime"

	"vartio/internal/common"
)

//go:embed official.json
var officialJSON []byte

// Artifact is one prebuilt package in a catalog. Target is the hub's
// GOOS-GOARCH. The sha256 covers the whole .vtp file.
type Artifact struct {
	Target       string `json:"target"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	SigningKeyID string `json:"signingKeyId"`
}

// CatalogItem is a plugin the hub can list. Official plugins ship inside the
// hub binary. The manifest is the contract; artifacts are the files.
type CatalogItem struct {
	Name      string
	Official  bool
	Origin    string
	RepoID    string
	Artifacts []Artifact
	Manifest
}

func DefaultCatalog() []CatalogItem {
	items, err := parseCatalog(officialJSON, repoEntry{ID: "vartio", Name: "Vartio 官方", KeyID: officialKeyID}, true)
	if err != nil {
		panic(err)
	}
	return items
}

func (r *Registry) catalogBy(name string) (CatalogItem, bool) {
	return r.lookup(name)
}

func hubTarget() string { return runtime.GOOS + "-" + runtime.GOARCH }

var targetName = regexp.MustCompile(`^(darwin|linux|windows)-(amd64|arm64)$`)

func pickArtifact(item CatalogItem) (Artifact, error) {
	target := hubTarget()
	for _, a := range item.Artifacts {
		if a.Target == target {
			return a, nil
		}
	}
	return Artifact{}, common.ErrBad("没有适用于 %s 的安装包", target)
}
