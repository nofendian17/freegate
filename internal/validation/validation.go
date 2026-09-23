package validation

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"
)

var validate = newValidator()
var resourceNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func newValidator() *validator.Validate {
	validate := validator.New(validator.WithRequiredStructEnabled())
	validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	if err := validate.RegisterValidation("resource_name", func(field validator.FieldLevel) bool {
		value, ok := field.Field().Interface().(string)
		return ok && resourceNamePattern.MatchString(value)
	}); err != nil {
		panic(err)
	}
	if err := validate.RegisterValidation("proxy_mode", func(field validator.FieldLevel) bool {
		value := field.Field()
		for value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return true
			}
			value = value.Elem()
		}
		if value.Kind() != reflect.String {
			return false
		}
		switch value.String() {
		case "", "direct", "pool", "global":
			return true
		default:
			return false
		}
	}, true); err != nil {
		panic(err)
	}
	if err := validate.RegisterValidation("http_headers", func(field validator.FieldLevel) bool {
		headers, ok := field.Field().Interface().(map[string]string)
		if !ok {
			return false
		}
		for name, value := range headers {
			if !validHTTPHeaderName(name) || strings.ContainsAny(value, "\r\n") {
				return false
			}
		}
		return true
	}, true); err != nil {
		panic(err)
	}
	return validate
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", character):
		default:
			return false
		}
	}
	return true
}

func Struct(value any) error {
	return validate.Struct(value)
}

func Field(value any, rules string) error {
	return validate.Var(value, rules)
}
