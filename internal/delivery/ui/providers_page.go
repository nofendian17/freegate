package ui

import "net/http"

type providersPageData struct {
	Title      string
	Active     string
	Meta       bool
	Playground bool
	Palette    bool
	Scripts    []string
}

func (h *Handler) providersPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := providersPageData{
		Title:      "freegate — providers",
		Active:     "providers",
		Meta:       false,
		Playground: false,
		Palette:    true,
		Scripts:    []string{"/static/js/providers.js"},
	}
	if err := h.templates.ExecuteTemplate(w, "providers.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
