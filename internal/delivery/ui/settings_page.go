package ui

import "net/http"

type settingsPageData struct {
	Title      string
	Active     string
	Meta       bool
	Playground bool
	Scripts    []string
}

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := settingsPageData{
		Title:      "freegate — settings",
		Active:     "settings",
		Meta:       false,
		Playground: false,
		Scripts:    []string{"/static/js/ui.js", "/static/js/settings.js"},
	}
	if err := h.templates.ExecuteTemplate(w, "settings.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
