package ui

import "net/http"

type providersPageData struct {
	Title      string
	Active     string
	Meta       bool
	Playground bool
	Scripts    []string
}

func (h *Handler) providersPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := providersPageData{
		Title:      "freegate — providers",
		Active:     "providers",
		Meta:       false,
		Playground: false,
		Scripts:    []string{"/static/js/ui.js", "/static/js/providers.js"},
	}
	if err := h.templates.ExecuteTemplate(w, "providers.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
