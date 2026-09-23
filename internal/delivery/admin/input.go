package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/providers"
	appvalidation "freegate/internal/validation"
)

const maxBodyLen = 1 << 20

type inputSanitizer interface {
	Sanitize()
}

func decodeInput(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyLen))
	if err := decoder.Decode(destination); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", "request body must contain one JSON object")
		return false
	}
	if sanitizer, ok := destination.(inputSanitizer); ok {
		sanitizer.Sanitize()
	}
	if err := validateInput(destination); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", validationMessage(err))
		return false
	}
	return true
}

func validateInput(value any) error {
	return appvalidation.Struct(value)
}

func validationMessage(err error) string {
	var fieldErrors validator.ValidationErrors
	if !errors.As(err, &fieldErrors) {
		return "invalid request"
	}
	messages := make([]string, 0, len(fieldErrors))
	for _, fieldError := range fieldErrors {
		messages = append(messages, fmt.Sprintf("%s failed %s validation", fieldError.Field(), fieldError.Tag()))
	}
	return strings.Join(messages, "; ")
}

func sanitizeResourceName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sanitizeProxyMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "global" {
		return ""
	}
	return value
}

func sanitizeTokens(values []string) []string {
	if values == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func sanitizeHeaders(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for name, value := range values {
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		result[name] = strings.TrimSpace(value)
	}
	return result
}

func (input *providerIn) Sanitize() {
	input.Name = sanitizeResourceName(input.Name)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.APIKeys = sanitizeTokens(input.APIKeys)
	input.Headers = sanitizeHeaders(input.Headers)
	if input.Models != nil {
		models := providers.NormalizeModels(*input.Models)
		input.Models = &models
	}
	if input.ProxyMode != nil {
		mode := sanitizeProxyMode(*input.ProxyMode)
		input.ProxyMode = &mode
	}
}

func (input *providerProbeIn) Sanitize() {
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.APIKeys = sanitizeTokens(input.APIKeys)
	input.Headers = sanitizeHeaders(input.Headers)
	input.ProxyMode = sanitizeProxyMode(input.ProxyMode)
}

func (input *poolIn) Sanitize() {
	input.Name = sanitizeResourceName(input.Name)
	input.ProxyURL = strings.TrimSpace(input.ProxyURL)
	input.NoProxy = strings.TrimSpace(input.NoProxy)
}

func (input *comboIn) Sanitize() {
	input.Name = sanitizeResourceName(input.Name)
	for index := range input.Tiers {
		input.Tiers[index].Provider = strings.TrimSpace(input.Tiers[index].Provider)
		input.Tiers[index].Model = strings.TrimSpace(input.Tiers[index].Model)
	}
}

func (input *clientKeyIn) Sanitize() {
	input.Name = sanitizeResourceName(input.Name)
}

func (input *clientKeyUpdateIn) Sanitize() {
	input.Name = sanitizeResourceName(input.Name)
}

func (input *builtinProxyIn) Sanitize() {
	input.ProxyMode = sanitizeProxyMode(input.ProxyMode)
}

func (input *vercelDeployIn) Sanitize() {
	input.VercelToken = strings.TrimSpace(input.VercelToken)
	input.ProjectName = sanitizeResourceName(input.ProjectName)
}
