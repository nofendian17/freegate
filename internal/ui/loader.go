package ui

import (
	"html/template"
	"io/fs"
	"strings"
	"time"
)

// LoadTemplates parses all embedded templates from the given FS.
// Templates are registered under their base name (e.g. "dashboard.html",
// "partials/stats.html") so callers can address them without embedding paths.
func LoadTemplates(fsys fs.FS) (*template.Template, error) {
	funcs := template.FuncMap{
		"fmtTime": func(t time.Time) string { return t.Format("15:04:05") },
		"shorten": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
	}

	tpl := template.New("").Funcs(funcs)

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() {
			// recurse into subdirectories (e.g. "partials")
			subEntries, err := fs.ReadDir(fsys, e.Name())
			if err != nil {
				return nil, err
			}
			for _, sub := range subEntries {
				if sub.IsDir() || !strings.HasSuffix(sub.Name(), ".html") {
					continue
				}
				path := e.Name() + "/" + sub.Name()
				body, err := fs.ReadFile(fsys, path)
				if err != nil {
					return nil, err
				}
				if _, err := tpl.New(path).Parse(string(body)); err != nil {
					return nil, err
				}
			}
		} else if strings.HasSuffix(e.Name(), ".html") {
			body, err := fs.ReadFile(fsys, e.Name())
			if err != nil {
				return nil, err
			}
			if _, err := tpl.New(e.Name()).Parse(string(body)); err != nil {
				return nil, err
			}
		}
	}
	return tpl, nil
}
