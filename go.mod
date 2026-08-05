module github.com/sei-protocol/omnigent-go-sdk

// The language floor, not a toolchain preference. This package's own code needs
// 1.23 — the release that introduced `iter.Seq2` and range-over-func — and
// nothing newer; the line reads 1.24.0 because oapi-codegen/runtime declares
// that, and `go mod tidy` will not let a module claim a lower floor than its
// dependencies. Raise it only when something here genuinely requires more: a
// library that pins high only narrows who can import it. No `toolchain`
// directive on purpose — one would make consumers silently download a different
// Go than the one they installed.
go 1.24.0

require github.com/oapi-codegen/runtime v1.6.0

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
)
