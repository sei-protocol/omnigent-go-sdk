package omnigent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// docLink matches a go-doc reference: [Name] or [Type.Member].
var docLink = regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\]`)

// stdPackagesInDocLinks are the packages a qualified link may name, as in
// http.Request. Listed rather than resolved, because resolving imports to check a
// comment would cost more than the check is worth.
var stdPackagesInDocLinks = map[string]bool{
	"ast": true, "atomic": true, "bufio": true, "bytes": true, "context": true,
	"errors": true, "fmt": true, "http": true, "io": true, "iter": true,
	"json": true, "mime": true, "multipart": true, "net": true, "os": true,
	"runtime": true, "strings": true, "sync": true, "testing": true,
	"textproto": true, "time": true, "tls": true, "url": true, "utf8": true,
}

// TestEveryDocLinkResolves pins that a go-doc reference names something that
// exists.
//
// A dangling link renders as literal brackets and sends a reader looking for a
// symbol that is not there. The costly version names the wrong owner — a link to
// Type.Method where Method belongs to some other type — because it reads as
// authoritative and is wrong. So a member link is checked against the member
// itself, and is never satisfied by the owning type alone.
func TestEveryDocLinkResolves(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(paths) < 10 {
		t.Fatalf("found only %d Go files; the scan is not reading the package", len(paths))
	}

	// Parsed one file at a time rather than with parser.ParseDir, whose
	// ast.Package return is deprecated.
	files := make([]*ast.File, 0, len(paths))
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", path, err)
		}
		files = append(files, file)
	}

	names, members, isType := declaredIdentifiers(files)

	for _, file := range files {
		for _, group := range file.Comments {
			for _, comment := range group.List {
				for _, ref := range unresolvedRefs(comment.Text, names, members, isType) {
					pos := fset.Position(comment.Pos())
					t.Errorf("%s:%d: doc link [%s] names nothing in this package",
						filepath.Base(pos.Filename), pos.Line, ref)
				}
			}
		}
	}
}

// declaredIdentifiers collects every name the package declares: top-level names,
// struct fields, interface methods, and Type.Member pairs.
func declaredIdentifiers(files []*ast.File) (names, members, isType map[string]bool) {
	names, members, isType = map[string]bool{}, map[string]bool{}, map[string]bool{}

	recordStruct := func(owner string, st *ast.StructType) {
		for _, field := range st.Fields.List {
			for _, n := range field.Names {
				names[n.Name] = true
				members[owner+"."+n.Name] = true
			}
		}
	}

	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				names[d.Name.Name] = true
				if d.Recv == nil || len(d.Recv.List) == 0 {
					continue
				}
				recv := d.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if id, ok := recv.(*ast.Ident); ok {
					members[id.Name+"."+d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names[s.Name.Name] = true
						isType[s.Name.Name] = true
						switch t := s.Type.(type) {
						case *ast.StructType:
							recordStruct(s.Name.Name, t)
						case *ast.InterfaceType:
							for _, m := range t.Methods.List {
								for _, n := range m.Names {
									members[s.Name.Name+"."+n.Name] = true
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							names[n.Name] = true
						}
					}
				}
			}
		}
	}
	return names, members, isType
}

// unresolvedRefs returns the doc links in one comment that name nothing.
//
// Two shapes are bracketed text rather than links, and are skipped so the check
// needs no per-line allowlist: a type parameter in map[string]any, and an
// upstream sentinel such as [DONE] or "[REDACTED]". No identifier in this package
// is spelled in upper case, so that rule costs no coverage.
func unresolvedRefs(text string, names, members, isType map[string]bool) []string {
	var bad []string
	for _, m := range docLink.FindAllStringSubmatchIndex(text, -1) {
		ref := text[m[2]:m[3]]
		if strings.HasSuffix(text[:m[0]], "map") {
			continue
		}
		if ref == strings.ToUpper(ref) {
			continue
		}
		if owner, member, ok := strings.Cut(ref, "."); ok {
			// A member must exist in its own right. Accepting the owner alone is
			// what lets a link name a method its type does not have.
			if members[ref] || stdPackagesInDocLinks[owner] {
				continue
			}
			if !isType[owner] && names[owner] {
				continue
			}
			_ = member
		} else if names[ref] {
			continue
		}
		bad = append(bad, ref)
	}
	return bad
}

// TestThePackageDocIsNotSevered pins that every comment line above the package
// clause reaches godoc.
//
// A doc comment is the one comment group immediately before `package`, so a
// single blank line inside it silently truncates the published documentation at
// that point. This package's own doc.go carried such a line: 139 of 256 lines
// and six of eleven sections stopped rendering, and `go doc .` opened on a
// symbol the package deliberately does not have.
//
// Nothing else catches it. gofmt does not mind, and [TestEveryDocLinkResolves]
// walks every comment group rather than the attached one, so the orphaned half
// still passes its link check.
func TestThePackageDocIsNotSevered(t *testing.T) {
	t.Parallel()

	const doc = "doc.go"
	raw, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	lines := strings.Split(string(raw), "\n")

	pkg := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "package ") })
	if pkg < 0 {
		t.Fatalf("%s declares no package", doc)
	}

	// Walk back over the group that actually reaches godoc.
	attached := 0
	for i := pkg - 1; i >= 0 && strings.HasPrefix(lines[i], "//"); i-- {
		attached++
	}
	total := 0
	for _, line := range lines[:pkg] {
		if strings.HasPrefix(line, "//") {
			total++
		}
	}
	if total < 50 {
		t.Fatalf("found only %d comment lines above the package clause; the scan is wrong", total)
	}
	if attached != total {
		t.Errorf("%d of %d comment lines reach godoc: a blank line inside the package "+
			"comment truncates it at line %d. Use // for a spacer line.",
			attached, total, pkg-attached)
	}
}
