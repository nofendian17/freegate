package ui

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// apiVPNServers returns the relay servers currently offered by the
// supervisor so the dashboard can render a picker.
func (h *Handler) apiVPNServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.vpn.ListServers()
	if err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeVPNJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

// apiVPNRefreshServers forces the supervisor to re-fetch the live vpngate
// relay list and returns the freshly filtered set for the picker.
func (h *Handler) apiVPNRefreshServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.vpn.RefreshServers()
	if err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeVPNJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

// apiVPNStatus returns the current tunnel state plus the active route
// mode (direct vs tunnel).
func (h *Handler) apiVPNStatus(w http.ResponseWriter, r *http.Request) {
	st, err := h.vpn.Status()
	if err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeVPNJSON(w, http.StatusOK, map[string]any{
		"direct":       h.vpn.Direct(),
		"connected":    st.Connected,
		"server":       st.Server,
		"country":      st.Country,
		"ip":           st.IP,
		"connected_at": st.ConnectedAt,
	})
}

// apiVPNDirect switches the proxy between direct connections and the VPN
// tunnel at runtime: {"direct": true} routes upstream traffic straight from
// the proxy container, {"direct": false} back through the tunnel.
func (h *Handler) apiVPNDirect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Direct *bool `json:"direct"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Direct == nil {
		writeVPNJSONError(w, http.StatusBadRequest, "body must be {\"direct\": true|false}")
		return
	}
	if err := h.vpn.SetDirect(*req.Direct); err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeVPNJSON(w, http.StatusOK, map[string]any{"direct": *req.Direct})
}

// apiVPNConnect connects the tunnel to a specific server the user picked:
// {"server": "<hostname>"}. Errors surface the supervisor's message so the
// dashboard can show why the connect failed (e.g. dead relay).
func (h *Handler) apiVPNConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Server string `json:"server"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Server == "" {
		writeVPNJSONError(w, http.StatusBadRequest, "body must be {\"server\": \"<hostname>\"}")
		return
	}
	if err := h.vpn.ConnectTo(req.Server); err != nil {
		slog.Warn("vpn: manual connect failed", "server", req.Server, "error", err)
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	st, err := h.vpn.Status()
	if err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeVPNJSON(w, http.StatusOK, st)
}

// apiVPNPing runs a live connectivity check through the tunnel (DNS +
// HTTPS egress + latency) so the dashboard can verify the VPN actually
// routes traffic. The route mode is attached to the result: in direct
// mode the ping reflects the tunnel, not the active traffic path, and the
// dashboard must say so.
func (h *Handler) apiVPNPing(w http.ResponseWriter, r *http.Request) {
	res, err := h.vpn.Ping()
	if err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	res.Direct = h.vpn.Direct()
	writeVPNJSON(w, http.StatusOK, res)
}

// apiVPNRotate disconnects and connects to a different random server
// (manual equivalent of the old automatic 429 rotation).
func (h *Handler) apiVPNRotate(w http.ResponseWriter, r *http.Request) {
	if err := h.vpn.ForceNewIP(); err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	st, err := h.vpn.Status()
	if err != nil {
		writeVPNJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeVPNJSON(w, http.StatusOK, st)
}

func writeVPNJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeVPNJSONError(w http.ResponseWriter, status int, msg string) {
	writeVPNJSON(w, status, map[string]any{"error": msg})
}
