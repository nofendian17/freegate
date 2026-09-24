package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/registry"
)

const vercelAPI = "https://api.vercel.com"

type vercelDeployIn struct {
	VercelToken string `json:"vercel_token" validate:"required,max=4096"`
	ProjectName string `json:"project_name" validate:"omitempty,resource_name"`
}

const vercelRelayCode = `
export const config = { runtime: "edge" };

export default async function handler(req) {
  const target = req.headers.get("x-relay-target");
  const relayPath = req.headers.get("x-relay-path") || "/";
  if (!target) {
    return new Response(JSON.stringify({ error: "Missing x-relay-target header" }), {
      status: 400,
      headers: { "content-type": "application/json" },
    });
  }

  const targetUrl = target.replace(/\/$/, "") + relayPath;

  const headers = new Headers(req.headers);
  headers.delete("x-relay-target");
  headers.delete("x-relay-path");
  headers.delete("host");
  // Strip client-IP headers so the upstream only sees the relay's edge
  // egress IP, never the original client IP.
  for (const h of ["forwarded", "x-forwarded-for", "x-forwarded-host",
    "x-real-ip", "true-client-ip", "cf-connecting-ip",
    "x-vercel-proxied-for", "x-vercel-ip-city", "x-vercel-ip-country",
    "x-vercel-ip-country-region", "x-vercel-ip-continent",
    "x-vercel-ip-latitude", "x-vercel-ip-longitude",
    "x-vercel-ip-postal-code", "x-vercel-ip-timezone",
    "x-vercel-ip-as-number", "x-vercel-ja4-digest"]) {
    headers.delete(h);
  }

  const response = await fetch(targetUrl, {
    method: req.method,
    headers,
    body: req.method !== "GET" && req.method !== "HEAD" ? req.body : undefined,
    duplex: "half",
  });

  return new Response(response.body, {
    status: response.status,
    headers: response.headers,
  });
}
`

func relayProjectName(name string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return "relay-" + strconv.FormatInt(time.Now().UnixMilli(), 36)
}

func platformRequest(ctx context.Context, client *http.Client, method, url, token string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyLen))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

func pollDeployReady(ctx context.Context, client *http.Client, url, token string) (map[string]any, error) {
	for i := 0; i < 40; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
		status, raw, err := platformRequest(ctx, client, http.MethodGet, url, token, nil)
		if err != nil {
			return nil, err
		}
		data := map[string]any{}
		if status == http.StatusOK {
			_ = json.Unmarshal(raw, &data)
		}
		var readyState string
		if s, ok := data["readyState"].(string); ok {
			readyState = s
		}
		switch readyState {
		case "READY":
			return data, nil
		case "ERROR", "CANCELED":
			return data, fmt.Errorf("Deployment failed: %s", readyState)
		}
	}
	return nil, fmt.Errorf("deployment timed out")
}

func deployClient(h *Handler) *http.Client {
	if h.transport != nil {
		return &http.Client{Transport: h.transport, Timeout: 30 * time.Second}
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// deployPoolToVercel deploys the edge relay function and stores the
// resulting pool. The Vercel token is request-scoped only, never stored.
func (h *Handler) deployPoolToVercel(w http.ResponseWriter, r *http.Request) {
	var reqBody vercelDeployIn
	if !decodeInput(w, r, &reqBody) {
		return
	}
	vercelToken := reqBody.VercelToken
	projectName := relayProjectName(reqBody.ProjectName)
	client := deployClient(h)

	pkgJSON, _ := json.Marshal(map[string]any{"name": projectName, "version": "1.0.0"})
	vercelJSON, _ := json.Marshal(map[string]any{
		"rewrites": []any{map[string]any{"source": "/(.*)", "destination": "/api/relay"}},
	})
	deployBody, _ := json.Marshal(map[string]any{
		"name": projectName,
		"files": []any{
			map[string]any{"file": "api/relay.js", "data": vercelRelayCode},
			map[string]any{"file": "package.json", "data": string(pkgJSON)},
			map[string]any{"file": "vercel.json", "data": string(vercelJSON)},
		},
		"projectSettings": map[string]any{"framework": nil},
		"target":          "production",
	})

	status, raw, err := platformRequest(r.Context(), client, http.MethodPost, vercelAPI+"/v13/deployments", vercelToken, deployBody)
	if err != nil {
		respond.JSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	if status < 200 || status >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = "Failed to create Vercel deployment"
		}
		respond.JSONError(w, status, "upstream_error", msg)
		return
	}
	var deployment struct {
		ID        string `json:"id"`
		UID       string `json:"uid"`
		ProjectID string `json:"projectId"`
	}
	_ = json.Unmarshal(raw, &deployment)
	deploymentID := deployment.ID
	if deploymentID == "" {
		deploymentID = deployment.UID
	}
	projectID := deployment.ProjectID
	if projectID == "" {
		projectID = projectName
	}

	if p, err := json.Marshal(map[string]any{"ssoProtection": nil}); err == nil {
		_, _, _ = platformRequest(r.Context(), client, http.MethodPatch, vercelAPI+"/v9/projects/"+projectID, vercelToken, p)
	}

	ready, err := pollDeployReady(r.Context(), client, vercelAPI+"/v13/deployments/"+deploymentID, vercelToken)
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "deploy_error", err.Error())
		return
	}
	urlStr, _ := ready["url"].(string)
	deployURL := "https://" + urlStr

	row, err := h.store.CreatePool(r.Context(), registry.ProxyPool{Name: projectName, ProxyURL: deployURL, Enabled: true})
	if err != nil {
		respondStoreError(w, err)
		return
	}
	if err := h.rebuild(r.Context()); err != nil {
		respondRebuildError(w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{"pool": row, "deploy_url": deployURL})
}
