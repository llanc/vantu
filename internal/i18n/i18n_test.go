package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestTranslate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"密码错误", "Incorrect password"},
		{"管理员登录成功 (10.0.0.1)", "Admin signed in (10.0.0.1)"},
		{"已将 demo.example.com 切换为 DNS 直连", "Switched demo.example.com to DNS only"},
		{"已将 demo.example.com 切换为 Cloudflare CDN", "Switched demo.example.com to Cloudflare CDN"},
		{`节点名称 "hk" 已存在`, `Node name "hk" already exists`},
		{"DNS 凭据校验失败：节点下仍绑定 3 条反代路由，请先迁移或删除对应路由",
			"DNS credential verification failed: The node still has 3 routes; move or delete them first"},
		{"边缘节点: 香港", "Edge node: 香港"},
		{"no chinese here", "no chinese here"},
		{"完全未知的中文消息", "完全未知的中文消息"},
	}
	for _, c := range cases {
		if got := Translate(EN, c.in); got != c.want {
			t.Errorf("Translate(%q)\n got: %q\nwant: %q", c.in, got, c.want)
		}
	}
	if got := Translate(ZH, "密码错误"); got != "密码错误" {
		t.Errorf("zh must be passthrough, got %q", got)
	}
}

func TestRenderIndexed(t *testing.T) {
	if got := render("%[2]s then %[1]s", []string{"a", "b"}); got != "b then a" {
		t.Fatalf("render = %q", got)
	}
	if got := render("100%% of %s", []string{"x"}); got != "100% of x" {
		t.Fatalf("render = %q", got)
	}
}

func TestLocalizeJSON(t *testing.T) {
	in := []byte(`{"ok":false,"error":{"code":"AUTH_REQUIRED","message":"密码错误"},"meta":{"n":12345678901234567890}}`)
	out := string(LocalizeJSON(EN, in))
	if !strings.Contains(out, `"message":"Incorrect password"`) || !strings.Contains(out, `12345678901234567890`) {
		t.Fatalf("unexpected output: %s", out)
	}
	if got := LocalizeJSON(ZH, in); string(got) != string(in) {
		t.Fatalf("zh must be passthrough")
	}
}

func TestFromRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?lang=en", nil)
	if FromRequest(r) != EN {
		t.Fatal("query lang not honored")
	}
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(HeaderName, "en-US")
	if FromRequest(r) != EN {
		t.Fatal("header lang not honored")
	}
	if FromRequest(httptest.NewRequest("GET", "/x", nil)) != ZH {
		t.Fatal("default must be zh")
	}
}

var anyVerb = regexp.MustCompile(`%[sdvqw]`)

func normalizeVerbs(s string) string {
	return anyVerb.ReplaceAllStringFunc(s, func(v string) string {
		if v == "%d" {
			return v
		}
		return "%s"
	})
}

func TestCatalogVerbsMatch(t *testing.T) {
	idx := regexp.MustCompile(`%(\[\d+\])?[sdvqw]`)
	for zh, en := range catalog {
		if a, b := len(anyVerb.FindAllString(zh, -1)), len(idx.FindAllString(en, -1)); a != b {
			t.Errorf("verb count mismatch (%d vs %d): %q -> %q", a, b, zh, en)
		}
	}
}

// Every Chinese string literal in packages whose output reaches the Web console
// must be covered by the catalog, otherwise English users would see Chinese.
func TestCatalogCoversSources(t *testing.T) {
	dirs := []string{"api", "orchestrator", "prober", "discovery", "dns", "caddy", "store", "update"}
	ignore := map[string]bool{"默认": true} // DNSPod "RecordLine" API value
	keys := make([]string, 0, len(catalog))
	for k := range catalog {
		keys = append(keys, normalizeVerbs(k))
	}

	scanned := 0
	defer func() {
		if scanned < 150 {
			t.Errorf("only %d Chinese literals scanned, source scan looks broken", scanned)
		}
	}()
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join("..", d, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			fset := token.NewFileSet()
			node, err := parser.ParseFile(fset, f, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(node, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !hasHan(s) || ignore[s] {
					return true
				}
				scanned++
				s = normalizeVerbs(s)
				for _, k := range keys {
					if strings.Contains(k, s) {
						return true
					}
				}
				t.Errorf("%s: missing English translation for %q", fset.Position(lit.Pos()), s)
				return true
			})
		}
	}
}
