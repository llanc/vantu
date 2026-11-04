package cli

import (
	"os"
	"strings"

	"github.com/llanc/vantu/internal/i18n"
)

// lang is resolved before the command tree is built so help texts are localized too.
var lang = resolveLang(os.Args[1:])

// resolveLang reads --lang from raw arguments (cobra has not parsed them yet), then VANTU_LANG.
func resolveLang(args []string) string {
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, "--lang="); ok {
			return i18n.Normalize(v)
		}
		if a == "--lang" && i+1 < len(args) {
			return i18n.Normalize(args[i+1])
		}
	}
	return i18n.Normalize(os.Getenv("VANTU_LANG"))
}

func tr(zh, en string) string {
	if lang == i18n.EN {
		return en
	}
	return zh
}
