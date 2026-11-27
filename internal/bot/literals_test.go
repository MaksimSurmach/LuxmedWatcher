package bot

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// User-facing text belongs in the locale files; a Cyrillic literal here is a
// string en and pl users would see in Russian.
func TestNoHardcodedCyrillic(t *testing.T) {
	allowed := map[string]bool{
		`"Русский"`: true, // language picker shows each language in itself
		`"все"`:     true, // accepted typed input, not output
		`"любое"`:   true,
	}
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && !allowed[lit.Value] && strings.IndexFunc(lit.Value, func(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }) >= 0 {
				t.Errorf("%s: hardcoded %s, move it to internal/i18n/locales", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}
