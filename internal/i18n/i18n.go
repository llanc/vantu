// Package i18n localizes backend messages for the Web console.
//
// Source strings stay in Chinese (the default locale). For English clients the
// API layer runs every Chinese string in a JSON response through Translate,
// which matches it against the zh→en catalog. Formatted messages are matched by
// turning their fmt verbs into capture groups, so messages already persisted in
// the database (audit log, route errors) are translated as well.
package i18n

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	ZH = "zh"
	EN = "en"

	HeaderName = "X-Vantu-Lang"
	CookieName = "vantu_lang"
)

// Normalize maps any language tag to a supported locale, defaulting to Chinese.
func Normalize(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if strings.HasPrefix(tag, "en") {
		return EN
	}
	return ZH
}

// FromRequest resolves the client locale from header, query string or cookie.
func FromRequest(r *http.Request) string {
	if v := r.Header.Get(HeaderName); v != "" {
		return Normalize(v)
	}
	if v := r.URL.Query().Get("lang"); v != "" {
		return Normalize(v)
	}
	if c, err := r.Cookie(CookieName); err == nil {
		return Normalize(c.Value)
	}
	return ZH
}

type pattern struct {
	re      *regexp.Regexp
	en      string
	literal int
}

var (
	exact    = map[string]string{}
	patterns []pattern
	verbRe   = regexp.MustCompile(`%[sdvqw]`)
)

func init() {
	for zh, en := range catalog {
		if !verbRe.MatchString(zh) {
			exact[zh] = en
			continue
		}
		var b strings.Builder
		b.WriteString("^")
		last := 0
		for _, loc := range verbRe.FindAllStringIndex(zh, -1) {
			b.WriteString(regexp.QuoteMeta(zh[last:loc[0]]))
			if zh[loc[1]-1] == 'd' {
				b.WriteString(`(-?\d+)`)
			} else {
				b.WriteString(`(.*?)`)
			}
			last = loc[1]
		}
		b.WriteString(regexp.QuoteMeta(zh[last:]))
		b.WriteString("$")
		patterns = append(patterns, pattern{
			re:      regexp.MustCompile("(?s)" + b.String()),
			en:      en,
			literal: len(verbRe.ReplaceAllString(zh, "")),
		})
	}
	sort.SliceStable(patterns, func(i, j int) bool { return patterns[i].literal > patterns[j].literal })
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// Translate returns s in the requested locale. Unknown strings are returned unchanged.
func Translate(lang, s string) string {
	if lang != EN || !hasHan(s) {
		return s
	}
	if v, ok := exact[s]; ok {
		return v
	}
	for _, p := range patterns {
		m := p.re.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		args := m[1:]
		for i := range args {
			args[i] = Translate(lang, args[i])
		}
		return render(p.en, args)
	}
	return s
}

// render substitutes args into an English format that uses %s-style verbs,
// optionally with explicit indexes (%[2]s) when the word order differs.
func render(format string, args []string) string {
	var b strings.Builder
	next := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' || i+1 >= len(format) {
			b.WriteByte(c)
			continue
		}
		if format[i+1] == '%' {
			b.WriteByte('%')
			i++
			continue
		}
		idx := next
		j := i + 1
		if format[j] == '[' {
			end := strings.IndexByte(format[j:], ']')
			if end > 0 {
				if n, err := strconv.Atoi(format[j+1 : j+end]); err == nil {
					idx = n - 1
				}
				j += end + 1
			}
		} else {
			next++
		}
		if j < len(format) && strings.IndexByte("sdvqw", format[j]) >= 0 {
			if idx >= 0 && idx < len(args) {
				b.WriteString(args[idx])
			}
			i = j
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// LocalizeJSON translates every Chinese string value inside a JSON document.
func LocalizeJSON(lang string, body []byte) []byte {
	if lang != EN || !hasHan(string(body)) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return body
	}
	out, err := json.Marshal(walk(lang, doc))
	if err != nil {
		return body
	}
	return append(out, '\n')
}

func walk(lang string, v any) any {
	switch t := v.(type) {
	case string:
		return Translate(lang, t)
	case []any:
		for i := range t {
			t[i] = walk(lang, t[i])
		}
	case map[string]any:
		for k := range t {
			t[k] = walk(lang, t[k])
		}
	}
	return v
}
