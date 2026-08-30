package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type nonLoopbackPlaintextRule struct {
	AddressProperty string `json:"addressProperty"`
	AllowProperty   string `json:"allowProperty"`
}

type schemaNode struct {
	Type                 string                    `json:"type"`
	Properties           map[string]schemaNode     `json:"properties"`
	Required             []string                  `json:"required"`
	AdditionalProperties *bool                     `json:"additionalProperties"`
	Default              json.RawMessage           `json:"default"`
	Const                json.RawMessage           `json:"const"`
	Enum                 []json.RawMessage         `json:"enum"`
	Minimum              *int64                    `json:"minimum"`
	Maximum              *int64                    `json:"maximum"`
	MinLength            *int                      `json:"minLength"`
	MaxLength            *int                      `json:"maxLength"`
	Pattern              string                    `json:"pattern"`
	DurationMinMS        *int64                    `json:"x-guard-duration-min-ms"`
	DurationMaxMS        *int64                    `json:"x-guard-duration-max-ms"`
	ListenAddress        bool                      `json:"x-guard-listen-address"`
	Owner                string                    `json:"x-guard-owner"`
	HotReload            *bool                     `json:"x-guard-hot-reload"`
	RestartRequired      *bool                     `json:"x-guard-restart-required"`
	Sensitive            *bool                     `json:"x-guard-sensitive"`
	NonLoopbackPlaintext *nonLoopbackPlaintextRule `json:"x-guard-non-loopback-plaintext"`
}

func parseSchema(contents []byte) (schemaNode, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var document struct {
		Schema               string                    `json:"$schema"`
		ID                   string                    `json:"$id"`
		Title                string                    `json:"title"`
		Description          string                    `json:"description"`
		Type                 string                    `json:"type"`
		AdditionalProperties *bool                     `json:"additionalProperties"`
		Required             []string                  `json:"required"`
		Properties           map[string]schemaNode     `json:"properties"`
		NonLoopbackPlaintext *nonLoopbackPlaintextRule `json:"x-guard-non-loopback-plaintext"`
	}
	if err := decoder.Decode(&document); err != nil {
		return schemaNode{}, fmt.Errorf("decode schema: %w", err)
	}
	if document.Schema != "https://json-schema.org/draft/2020-12/schema" {
		return schemaNode{}, fmt.Errorf("unsupported JSON Schema version")
	}
	return schemaNode{
		Type: document.Type, Properties: document.Properties, Required: document.Required,
		AdditionalProperties: document.AdditionalProperties,
		NonLoopbackPlaintext: document.NonLoopbackPlaintext,
	}, nil
}

func (s schemaNode) validate(document any) (any, error) {
	validated, err := validateSchemaValue("$", document, s)
	if err != nil {
		return nil, err
	}
	if s.NonLoopbackPlaintext != nil {
		if err := validateNonLoopbackPlaintext(validated, *s.NonLoopbackPlaintext); err != nil {
			return nil, err
		}
	}
	return validated, nil
}

func validateSchemaValue(path string, value any, node schemaNode) (any, error) {
	if err := validateLiteralConstraints(path, value, node); err != nil {
		return nil, err
	}
	switch node.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be an object", path)
		}
		unknown := make([]string, 0)
		if node.AdditionalProperties != nil && !*node.AdditionalProperties {
			for key := range object {
				if _, exists := node.Properties[key]; !exists {
					unknown = append(unknown, key)
				}
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, fmt.Errorf("unknown field %s", childPath(path, unknown[0]))
		}

		keys := make([]string, 0, len(node.Properties))
		for key := range node.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := node.Properties[key]
			childValue, exists := object[key]
			if !exists && len(child.Default) > 0 {
				decoded, err := decodeSchemaLiteral(child.Default)
				if err != nil {
					return nil, fmt.Errorf("schema default %s: %w", childPath(path, key), err)
				}
				object[key] = decoded
				childValue = decoded
				exists = true
			}
			if !exists {
				continue
			}
			validated, err := validateSchemaValue(childPath(path, key), childValue, child)
			if err != nil {
				return nil, err
			}
			object[key] = validated
		}
		for _, required := range node.Required {
			if _, exists := object[required]; !exists {
				return nil, fmt.Errorf("required field %s is missing", childPath(path, required))
			}
		}
		return object, nil

	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("%s must be an integer", path)
		}
		integer, err := number.Int64()
		if err != nil {
			return nil, fmt.Errorf("%s must be an integer", path)
		}
		if node.Minimum != nil && integer < *node.Minimum {
			return nil, fmt.Errorf("%s is below its schema minimum", path)
		}
		if node.Maximum != nil && integer > *node.Maximum {
			return nil, fmt.Errorf("%s exceeds its schema maximum", path)
		}
		return value, nil

	case "string":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a string", path)
		}
		length := utf8.RuneCountInString(text)
		if node.MinLength != nil && length < *node.MinLength {
			return nil, fmt.Errorf("%s is shorter than its schema minimum", path)
		}
		if node.MaxLength != nil && length > *node.MaxLength {
			return nil, fmt.Errorf("%s exceeds its schema maximum length", path)
		}
		if node.Pattern != "" {
			pattern, err := regexp.Compile(node.Pattern)
			if err != nil {
				return nil, fmt.Errorf("schema pattern for %s is invalid: %w", path, err)
			}
			if !pattern.MatchString(text) {
				return nil, fmt.Errorf("%s does not match its schema pattern", path)
			}
		}
		if node.DurationMinMS != nil || node.DurationMaxMS != nil {
			duration, err := time.ParseDuration(text)
			if err != nil {
				return nil, fmt.Errorf("%s is not a duration", path)
			}
			milliseconds := duration.Milliseconds()
			if node.DurationMinMS != nil && milliseconds < *node.DurationMinMS {
				return nil, fmt.Errorf("%s is below its schema duration minimum", path)
			}
			if node.DurationMaxMS != nil && milliseconds > *node.DurationMaxMS {
				return nil, fmt.Errorf("%s exceeds its schema duration maximum", path)
			}
		}
		if node.ListenAddress {
			if _, err := parseListenAddress(text); err != nil {
				return nil, fmt.Errorf("%s is not a valid listen address", path)
			}
		}
		return value, nil

	case "boolean":
		if _, ok := value.(bool); !ok {
			return nil, fmt.Errorf("%s must be a boolean", path)
		}
		return value, nil

	default:
		return nil, fmt.Errorf("schema type for %s is unsupported", path)
	}
}

func validateLiteralConstraints(path string, value any, node schemaNode) error {
	if len(node.Const) > 0 {
		expected, err := decodeSchemaLiteral(node.Const)
		if err != nil {
			return fmt.Errorf("schema const for %s: %w", path, err)
		}
		if !reflect.DeepEqual(value, expected) {
			return fmt.Errorf("%s does not equal its schema constant", path)
		}
	}
	if len(node.Enum) == 0 {
		return nil
	}
	for _, encoded := range node.Enum {
		candidate, err := decodeSchemaLiteral(encoded)
		if err != nil {
			return fmt.Errorf("schema enum for %s: %w", path, err)
		}
		if reflect.DeepEqual(value, candidate) {
			return nil
		}
	}
	return fmt.Errorf("%s is not in its schema enum", path)
}

func decodeSchemaLiteral(encoded []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func validateNonLoopbackPlaintext(document any, rule nonLoopbackPlaintextRule) error {
	addressValue, ok := valueAtPath(document, rule.AddressProperty)
	if !ok {
		return fmt.Errorf("schema security property %s is missing", rule.AddressProperty)
	}
	allowValue, ok := valueAtPath(document, rule.AllowProperty)
	if !ok {
		return fmt.Errorf("schema security property %s is missing", rule.AllowProperty)
	}
	address, addressOK := addressValue.(string)
	allow, allowOK := allowValue.(bool)
	if !addressOK || !allowOK {
		return fmt.Errorf("schema non-loopback plaintext rule has incompatible property types")
	}
	host, err := parseListenAddress(address)
	if err != nil {
		return fmt.Errorf("%s is not a valid listen address", rule.AddressProperty)
	}
	if !allow && !isLoopbackHost(host) {
		return fmt.Errorf(
			"non-loopback plaintext listener requires explicit %s=true",
			rule.AllowProperty)
	}
	return nil
}

func parseListenAddress(value string) (string, error) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("port is outside 1-65535")
	}
	return host, nil
}

func isLoopbackHost(host string) bool {
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && address.IsLoopback()
}

func valueAtPath(document any, path string) (any, bool) {
	current := document
	for _, component := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[component]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func childPath(parent, child string) string {
	if parent == "$" {
		return child
	}
	return parent + "." + child
}
