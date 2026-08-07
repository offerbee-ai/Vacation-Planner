package applemaps

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The package is meant to be liftable into its own module without a rewrite, so
// it must not reach back into this repository — not from its source files and not
// from its tests, since a test-only dependency would break extraction just as
// surely.
//
// This is checked mechanically rather than by convention because the failure is
// silent: an accidental POI or iowrappers import compiles fine and only shows up
// as pain much later, when someone tries to move the package.
func TestPackageDoesNotImportTheHostRepository(t *testing.T) {
	const modulePath = "github.com/weihesdlegend/Vacation-planner"

	fset := token.NewFileSet()
	packages, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}
	if len(packages) == 0 {
		t.Fatal("parsed no packages; the test is not looking at the right directory")
	}

	filesChecked := 0
	for _, pkg := range packages {
		for filename, file := range pkg.Files {
			filesChecked++
			for _, imported := range file.Imports {
				path := strings.Trim(imported.Path.Value, `"`)
				if strings.HasPrefix(path, modulePath) {
					t.Errorf("%s imports %q; applemaps must stay free of repository dependencies", filename, path)
				}
			}
		}
	}

	// Guard against the check silently passing because nothing was parsed.
	if filesChecked < 5 {
		t.Errorf("only %d files checked, expected the whole package", filesChecked)
	}
}
