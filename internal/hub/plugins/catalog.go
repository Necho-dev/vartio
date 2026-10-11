package plugins

// CatalogItem is a plugin the hub knows how to download. Name is the route
// name (cdp, bitbrowser); Git is the repository the hub clones.
// Official marks a plugin shipped with the hub. Origin is the label shown
// on the card (the built-in catalog, a custom repository, or a manual install).
type CatalogItem struct {
	Name     string
	Git      string
	Official bool
	Origin   string
	RepoID   string
	Manifest
}

func DefaultCatalog() []CatalogItem {
	return []CatalogItem{
		{
			Name: "cdp", Git: "git@github.com:Necho-dev/vartio-plugin-chrome-devtools.git",
			Official: true, Origin: "Vartio 官方",
			Manifest: Manifest{
				Name:        "cdp",
				Title:       "Chrome DevTools",
				Version:     "1.0.0",
				Description: "发现、启动并经探针隧道接管远端机器上的 Chrome / Edge / Chromium / 比特浏览器窗口，提供 CDP 透明转发与在线调试",
				Primitives:  []string{"tunnel.tcp", "fs.which", "fs.read", "proc.spawn", "net.listeners"},
				Targets:     []string{"127.0.0.1:*", "[::1]:*"},
			},
		},
		{
			Name: "bitbrowser", Git: "git@github.com:Necho-dev/vartio-plugin-bitbrowser.git",
			Official: true, Origin: "Vartio 官方",
			Manifest: Manifest{
				Name:        "bitbrowser",
				Title:       "比特浏览器",
				Version:     "1.0.0",
				Description: "经探针 TCP 隧道调用远端机器上的比特浏览器本地 API（127.0.0.1:54345）",
				Primitives:  []string{"tunnel.tcp"},
				Targets:     []string{"127.0.0.1:54345"},
			},
		},
	}
}

func (r *Registry) catalogBy(name string) (CatalogItem, bool) {
	return r.lookup(name)
}
