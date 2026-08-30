package config

import (
	"errors"
	"fmt"
	"strings"

	configschema "github.com/lifei6671/guard-wall/schema"
)

// ErrFieldPolicyNotFound reports a path that is not a configuration leaf.
var ErrFieldPolicyNotFound = errors.New("configuration field policy not found")

// FieldPolicy is the immutable ownership and lifecycle policy declared for one
// configuration field by config-v1.schema.json.
type FieldPolicy struct {
	Owner           string
	HotReload       bool
	RestartRequired bool
	Sensitive       bool
}

// LookupFieldPolicy returns a copy of the authoritative policy for one JSON
// leaf path, such as "logging.level". Object-group paths are not fields.
func LookupFieldPolicy(path string) (FieldPolicy, error) {
	authority, err := parseSchema(configschema.ConfigV1())
	if err != nil {
		return FieldPolicy{}, fmt.Errorf("lookup field policy: parse authoritative schema: %w", err)
	}
	node, ok := schemaNodeAtPath(authority, path)
	if !ok || len(node.Properties) != 0 {
		return FieldPolicy{}, fmt.Errorf("%w: %s", ErrFieldPolicyNotFound, path)
	}
	if node.Owner == "" || node.HotReload == nil ||
		node.RestartRequired == nil || node.Sensitive == nil {
		return FieldPolicy{}, fmt.Errorf("lookup field policy %s: schema metadata is incomplete", path)
	}
	return FieldPolicy{
		Owner: node.Owner, HotReload: *node.HotReload,
		RestartRequired: *node.RestartRequired, Sensitive: *node.Sensitive,
	}, nil
}

func schemaNodeAtPath(root schemaNode, path string) (schemaNode, bool) {
	if strings.TrimSpace(path) == "" {
		return schemaNode{}, false
	}
	current := root
	for _, component := range strings.Split(path, ".") {
		if component == "" {
			return schemaNode{}, false
		}
		next, ok := current.Properties[component]
		if !ok {
			return schemaNode{}, false
		}
		current = next
	}
	return current, true
}
