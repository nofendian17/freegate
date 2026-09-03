package ui

import "net/http"

func (h *Handler) providersPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, "providers.html", map[string]any{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
