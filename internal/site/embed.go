// Package site is the monitoring UI embedded in probe-hub.
package site

import "embed"

// FS is the browser app. types/ and tsconfig.json stay out: they are for tsc only.
//
//go:embed index.html app.css modules assets vendor
var FS embed.FS
