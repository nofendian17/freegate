package ui

import (
	"html/template"
	"io/fs"
	"strings"
	"testing"
)

func mustLoadTemplates(t *testing.T) *template.Template {
	t.Helper()
	tpl, err := LoadTemplates(webTemplatesFS(t))
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

func tagBlock(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], close)
	if j < 0 {
		return ""
	}
	return s[i : i+j+len(close)]
}

func readStaticFile(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(webStaticFS(t), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
