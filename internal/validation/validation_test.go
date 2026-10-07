package validation

import (
	"strings"
	"testing"
)

type resourceNameStruct struct {
	Name string `json:"name" validate:"required,resource_name"`
}

type proxyModeStruct struct {
	Mode *string `json:"mode" validate:"omitempty,proxy_mode"`
}

type httpHeadersStruct struct {
	Headers map[string]string `json:"headers" validate:"omitempty,http_headers"`
}

type taggedStruct struct {
	Email string `json:"email" validate:"required,email"`
	Name  string `json:"-" validate:"required"`
}

func TestStruct_ResourceName(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "lowercase letters", value: "my-resource"},
		{name: "digits", value: "pool-01"},
		{name: "single char", value: "a"},
		{name: "empty", value: "", wantErr: true},
		{name: "uppercase", value: "MyResource", wantErr: true},
		{name: "underscore", value: "my_resource", wantErr: true},
		{name: "spaces", value: "my resource", wantErr: true},
		{name: "too long", value: strings.Repeat("a", 65), wantErr: true},
		{name: "max length", value: strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Struct(resourceNameStruct{Name: tt.value})
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("Struct(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "name") {
				t.Fatalf("expected error to reference json field name, got: %v", err)
			}
		})
	}
}

func TestStruct_ProxyMode(t *testing.T) {
	valid := []string{"", "direct", "pool", "global"}
	for _, mode := range valid {
		value := mode
		if err := Struct(proxyModeStruct{Mode: &value}); err != nil {
			t.Fatalf("expected mode %q to be valid, got: %v", mode, err)
		}
	}

	invalid := []string{"DIRECT", "Direct", "http", "socks5"}
	for _, mode := range invalid {
		value := mode
		if err := Struct(proxyModeStruct{Mode: &value}); err == nil {
			t.Fatalf("expected mode %q to be rejected", mode)
		}
	}

	if err := Struct(proxyModeStruct{Mode: nil}); err != nil {
		t.Fatalf("expected nil pointer to be valid, got: %v", err)
	}
}

func TestStruct_HTTPHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		wantErr bool
	}{
		{name: "valid", headers: map[string]string{"X-Custom-Header": "value", "Authorization": "Bearer token"}},
		{name: "token chars", headers: map[string]string{"X-Api!#$%&'*+.^_`|~Key": "v"}},
		{name: "empty name", headers: map[string]string{"": "v"}, wantErr: true},
		{name: "space in name", headers: map[string]string{"X Custom": "v"}, wantErr: true},
		{name: "non-ascii name", headers: map[string]string{"X-Ünicode": "v"}, wantErr: true},
		{name: "crlf in value", headers: map[string]string{"X-A": "value\r\nX-Evil: injected"}, wantErr: true},
		{name: "lf in value", headers: map[string]string{"X-A": "line\ninjection"}, wantErr: true},
		{name: "cr in value", headers: map[string]string{"X-A": "carriage\rreturn"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Struct(httpHeadersStruct{Headers: tt.headers})
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("Struct error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStruct_UsesJSONFieldNamesInErrors(t *testing.T) {
	err := Struct(taggedStruct{})
	if err == nil {
		t.Fatal("expected error for missing required fields")
	}
	if !strings.Contains(err.Error(), "email") {
		t.Fatalf("expected error to reference json name 'email', got: %v", err)
	}
	// json:"-" yields an empty tag name, so validator falls back to the Go field name.
	if !strings.Contains(err.Error(), "Name") {
		t.Fatalf("expected error to reference Go field name 'Name' as fallback, got: %v", err)
	}
}

func TestStruct_Invalid(t *testing.T) {
	if err := Struct(resourceNameStruct{Name: "Bad_Name"}); err == nil {
		t.Fatal("expected error for invalid resource name")
	}
}

func TestStruct_Nil(t *testing.T) {
	if err := Struct(nil); err == nil {
		t.Fatal("expected error when validating nil value")
	}
}

func TestStruct_NonStruct(t *testing.T) {
	if err := Struct("not a struct"); err == nil {
		t.Fatal("expected error when validating a non-struct value")
	}
}

func TestField(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		rules   string
		wantErr bool
	}{
		{name: "valid email", value: "user@example.com", rules: "email"},
		{name: "invalid email", value: "not-an-email", rules: "email", wantErr: true},
		{name: "valid resource name", value: "abc-123", rules: "resource_name"},
		{name: "invalid resource name", value: "ABC-123", rules: "resource_name", wantErr: true},
		{name: "required with value", value: "x", rules: "required"},
		{name: "required with empty", value: "", rules: "required", wantErr: true},
		{name: "required with nil", value: nil, rules: "required", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Field(tt.value, tt.rules)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("Field(%v, %q) error = %v, wantErr %v", tt.value, tt.rules, err, tt.wantErr)
			}
		})
	}
}

func TestField_UnknownRule(t *testing.T) {
	// validator panics on unknown rules rather than returning an error.
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for unknown validation rule")
		}
	}()
	_ = Field("x", "definitely_not_a_rule")
}

func TestValidHTTPHeaderName(t *testing.T) {
	valid := []string{"A", "a", "Z", "z", "0", "9", "X-Custom-Header", "Content-Type",
		"!", "#", "$", "%", "&", "'", "*", "+", "-", ".", "^", "_", "`", "|", "~"}
	for _, name := range valid {
		if !validHTTPHeaderName(name) {
			t.Fatalf("expected %q to be a valid header name", name)
		}
	}

	invalid := []string{"", "X Custom", "X\tTab", "X\nNewline", "X~Ünicode", "héllo", "name:"}
	for _, name := range invalid {
		if validHTTPHeaderName(name) {
			t.Fatalf("expected %q to be rejected as header name", name)
		}
	}
}
