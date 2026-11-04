package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/llanc/vantu/internal/i18n"
)

func TestResolveLang(t *testing.T) {
	t.Setenv("VANTU_LANG", "")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"route", "ls"}, i18n.ZH},
		{[]string{"route", "ls", "--lang", "en"}, i18n.EN},
		{[]string{"--lang=en-US", "doctor"}, i18n.EN},
		{[]string{"--", "--lang=en"}, i18n.ZH},
	}
	for _, c := range cases {
		if got := resolveLang(c.args); got != c.want {
			t.Errorf("resolveLang(%v) = %q, want %q", c.args, got, c.want)
		}
	}
	t.Setenv("VANTU_LANG", "en")
	if got := resolveLang(nil); got != i18n.EN {
		t.Errorf("VANTU_LANG not honored, got %q", got)
	}
}

func hasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

// Every Chinese literal in the CLI must be the first argument of tr() with an English counterpart.
func TestCLIStringsLocalized(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		paired := map[*ast.BasicLit]bool{}
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "tr" || len(call.Args) != 2 {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok {
				paired[lit] = true
			}
			if lit, ok := call.Args[1].(*ast.BasicLit); ok {
				if s, _ := strconv.Unquote(lit.Value); hasHan(s) {
					t.Errorf("%s: English text contains Chinese: %q", fset.Position(lit.Pos()), s)
				}
			}
			return true
		})
		ast.Inspect(node, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if s, _ := strconv.Unquote(lit.Value); hasHan(s) && !paired[lit] {
				t.Errorf("%s: Chinese literal without English counterpart: %q", fset.Position(lit.Pos()), s)
			}
			return true
		})
	}
}
