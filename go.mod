module github.com/sei-protocol/omnigent-go-sdk

// The language floor, not a toolchain preference. It matches sei-agent-driver,
// which is this module's consumer: a library floor above its consumer's blocks
// the import, and one below it forgoes what the consumer can already use for no
// one's benefit. The package itself needs only 1.23, for iter.Seq2 and
// range-over-func.
//
// This directive is what narrows who can import the module: a consumer on an
// older Go cannot build against a higher floor, and `go mod tidy` will not let
// the line drop below what a dependency declares. There are no dependencies, so
// the floor is ours alone to justify — and the justification is the consumer, not
// a language feature. Lower it if a second consumer needs an older Go.
//
// No `toolchain` directive, even though the driver carries one. A dependency's
// toolchain line does not reach its consumers — a consumer under
// GOTOOLCHAIN=auto ignores it and builds with the Go it has — so it is nothing a
// library needs to state. A stdlib advisory is keyed to the toolchain that
// builds, which a library never controls.
go 1.25.0
