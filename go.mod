module github.com/sei-protocol/omnigent-go-sdk

// The language floor, not a toolchain preference. This package needs 1.23, the
// release that introduced iter.Seq2 and range-over-func, and nothing newer.
//
// Raise it only when something here genuinely requires more. This directive is
// what narrows who can import the module: a consumer on an older Go cannot build
// against a higher floor, and `go mod tidy` will not let the line drop below what
// a dependency declares. There are no dependencies now, so the floor is ours
// alone to justify.
//
// No `toolchain` directive, but not for the reason once written here. A
// dependency's toolchain line does not reach its consumers — a consumer under
// GOTOOLCHAIN=auto ignores it and builds with the Go it has. The directive is
// simply nothing a library needs to state, and a stdlib advisory is keyed to the
// toolchain that builds, which a library never controls.
go 1.23.0
