// Package migrations carries Beru's PostgreSQL schema as embedded SQL so it
// ships inside the distroless image. go:embed cannot reach outside its own
// package directory, which is the only reason this file exists.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
