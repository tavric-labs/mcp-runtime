package regression

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// soleModuleExceptions lists pkg/ packages that one module may use alone. An
// entry needs a written reason; the default is to move single-consumer code to
// that consumer's internal/ tree.
var soleModuleExceptions = map[string]string{}

// TestPkgHoldsOnlyCodeSharedAcrossModules enforces the rule that pkg/ is for
// code more than one Go module needs. The root module and every services/*
// module are separate modules, so code used by one of them belongs in its
// internal/ tree. Test files count as consumers, and use through another pkg/
// package is attributed to that package's own consumers.
func TestPkgHoldsOnlyCodeSharedAcrossModules(t *testing.T) {
	root := repoRoot(t)
	const modulePath = "mcp-runtime/"
	direct := map[string]map[string]bool{}  // pkg import path -> consumer modules
	pkgUses := map[string]map[string]bool{} // pkg import path -> pkg import paths it imports
	fset := token.NewFileSet()

	moduleOf := func(dir string) string {
		for d := dir; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
				rel, _ := filepath.Rel(root, d)
				return rel
			}
			if d == root || d == filepath.Dir(d) {
				return "."
			}
		}
	}
	skipDir := map[string]bool{".git": true, ".claude": true, "node_modules": true, "inspirations": true, "prs": true, "graphify-out": true, "examples": true, "traefik-plugins": true}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		owner := modulePath + filepath.ToSlash(rel)
		module := moduleOf(filepath.Dir(path))
		for _, imp := range file.Imports {
			ip := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(ip, modulePath+"pkg/") {
				continue
			}
			if strings.HasPrefix(owner, modulePath+"pkg/") {
				if owner != ip {
					if pkgUses[owner] == nil {
						pkgUses[owner] = map[string]bool{}
					}
					pkgUses[owner][ip] = true
				}
				continue
			}
			if direct[ip] == nil {
				direct[ip] = map[string]bool{}
			}
			direct[ip][module] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Propagate: whoever uses a pkg package also uses what it imports.
	for changed := true; changed; {
		changed = false
		for user, used := range pkgUses {
			for dep := range used {
				for m := range direct[user] {
					if direct[dep] == nil {
						direct[dep] = map[string]bool{}
					}
					if !direct[dep][m] {
						direct[dep][m] = true
						changed = true
					}
				}
			}
		}
	}
	var problems []string
	for ip, mods := range direct {
		name := strings.TrimPrefix(ip, modulePath+"pkg/")
		if len(mods) >= 2 {
			continue
		}
		if _, ok := soleModuleExceptions[name]; ok {
			continue
		}
		only := make([]string, 0, len(mods))
		for m := range mods {
			only = append(only, m)
		}
		sort.Strings(only)
		problems = append(problems, "pkg/"+name+" is used only by "+strings.Join(only, ", ")+"; move it to that module's internal/ tree or add a reasoned soleModuleExceptions entry")
	}
	for name := range soleModuleExceptions {
		if mods := direct[modulePath+"pkg/"+name]; len(mods) >= 2 {
			problems = append(problems, "pkg/"+name+" is shared now; remove it from soleModuleExceptions")
		}
	}
	slices.Sort(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
