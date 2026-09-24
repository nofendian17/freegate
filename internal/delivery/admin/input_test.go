package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"freegate/internal/infrastructure/registry"
)

func TestDecodeInputSanitizesBeforeValidation(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/providers", bytes.NewBufferString(`{
		"name":" Acme ",
		"base_url":" https://api.acme.test/v1 ",
		"api_keys":[" sk-live-1 "],
		"refresh_sec":60
	}`))
	recorder := httptest.NewRecorder()
	var input providerIn
	if !decodeInput(recorder, request, &input) {
		t.Fatalf("decode failed: %s", recorder.Body.String())
	}
	if input.Name != "acme" || input.BaseURL != "https://api.acme.test/v1" || !reflect.DeepEqual(input.APIKeys, []string{"sk-live-1"}) {
		t.Fatalf("decoded input was not sanitized: %+v", input)
	}
}

func TestDecodeInputRejectsMultipleJSONValues(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/providers", bytes.NewBufferString(`{} {}`))
	recorder := httptest.NewRecorder()
	var input providerIn
	if decodeInput(recorder, request, &input) {
		t.Fatal("expected multiple JSON values to be rejected")
	}
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "one JSON object") {
		t.Fatalf("unexpected response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestInputSanitizers(t *testing.T) {
	models := []string{" model-b ", "model-a", "model-b", " "}
	mode := " GLOBAL "
	provider := providerIn{
		Name:        "  Acme  ",
		BaseURL:     "  https://api.acme.test/v1  ",
		APIKeys:     []string{" sk-live-1 ", "", "sk-live-2", "sk-live-1"},
		Headers:     map[string]string{" X-Trace ": " value ", "": "ignored"},
		Models:      &models,
		RefreshSec:  60,
		ProxyMode:   &mode,
		ProxyPoolID: nil,
	}
	provider.Sanitize()
	wantModels := []string{"model-b", "model-a"}
	wantKeys := []string{"sk-live-1", "sk-live-2"}
	wantHeaders := map[string]string{"X-Trace": "value"}
	if provider.Name != "acme" || provider.BaseURL != "https://api.acme.test/v1" {
		t.Fatalf("provider scalar sanitization = %+v", provider)
	}
	if !reflect.DeepEqual(provider.APIKeys, wantKeys) {
		t.Fatalf("api keys = %#v, want %#v", provider.APIKeys, wantKeys)
	}
	if !reflect.DeepEqual(provider.Headers, wantHeaders) {
		t.Fatalf("headers = %#v, want %#v", provider.Headers, wantHeaders)
	}
	if provider.Models == nil || !reflect.DeepEqual(*provider.Models, wantModels) {
		t.Fatalf("models = %#v, want %#v", provider.Models, wantModels)
	}
	if provider.ProxyMode == nil || *provider.ProxyMode != "" {
		t.Fatalf("proxy mode = %#v, want global", provider.ProxyMode)
	}
	if err := validateInput(&provider); err != nil {
		t.Fatalf("sanitized provider must validate: %v", err)
	}
}

func TestInputValidation(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{
			name: "provider",
			value: providerIn{
				Name:       "Bad Name",
				BaseURL:    "file:///tmp/provider",
				RefreshSec: 60,
				ProxyMode:  stringPointer("sideways"),
			},
		},
		{
			name: "provider headers",
			value: providerIn{
				Name:       "headers",
				BaseURL:    "https://headers.test/v1",
				Headers:    map[string]string{"X-Test": "safe\r\ninjected: value"},
				RefreshSec: 60,
			},
		},
		{
			name: "pool",
			value: poolIn{
				Name:     "bad name",
				ProxyURL: "socks5://127.0.0.1:1080",
			},
		},
		{
			name: "combo",
			value: comboIn{
				Name:  "bad name",
				Tiers: []registry.ComboTier{{Provider: ""}},
			},
		},
		{
			name:  "client key",
			value: clientKeyIn{Name: "bad name"},
		},
		{
			name:  "builtin proxy",
			value: builtinProxyIn{ProxyMode: "sideways"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateInput(test.value); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
