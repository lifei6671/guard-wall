package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"
)

const (
	maxYAMLDocumentBytes = 64 * 1024
	maxYAMLDocumentNodes = 512
	maxYAMLDocumentDepth = 32
)

func decodeYAMLDocument(reader io.Reader) (any, error) {
	contents, err := io.ReadAll(io.LimitReader(reader, maxYAMLDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read YAML document failed")
	}
	if len(contents) > maxYAMLDocumentBytes {
		return nil, fmt.Errorf("YAML document exceeds 64 KiB limit")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid YAML syntax")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, fmt.Errorf("document root is invalid")
	}
	nodeCount := 0
	if err := validateYAMLStructure(&root, make(map[*yaml.Node]struct{}), 0, &nodeCount); err != nil {
		return nil, err
	}

	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple YAML documents")
		} else {
			err = fmt.Errorf("invalid trailing YAML content")
		}
		return nil, fmt.Errorf("trailing content: %w", err)
	}

	var decoded any
	if err := root.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode YAML values: invalid YAML value")
	}
	return normalizeYAMLValue(decoded)
}

func validateYAMLStructure(node *yaml.Node, visited map[*yaml.Node]struct{}, depth int, nodeCount *int) error {
	if node == nil {
		return fmt.Errorf("YAML node is missing")
	}
	if depth > maxYAMLDocumentDepth {
		return fmt.Errorf("YAML document exceeds depth limit of %d", maxYAMLDocumentDepth)
	}
	if _, ok := visited[node]; ok {
		return nil
	}
	visited[node] = struct{}{}
	*nodeCount = *nodeCount + 1
	if *nodeCount > maxYAMLDocumentNodes {
		return fmt.Errorf("YAML document exceeds node limit of %d", maxYAMLDocumentNodes)
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode {
		return fmt.Errorf("YAML anchors and aliases are not supported")
	}
	if node.Style&yaml.TaggedStyle != 0 {
		return fmt.Errorf("explicit YAML tags are not supported")
	}

	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateYAMLStructure(child, visited, depth+1, nodeCount); err != nil {
				return err
			}
		}
		return nil

	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return fmt.Errorf("mapping has an incomplete key/value pair")
		}
		keys := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Value == "<<" || key.ShortTag() == "!!merge" {
				return fmt.Errorf("YAML merge keys are not supported")
			}
			if err := validateYAMLStructure(key, visited, depth+1, nodeCount); err != nil {
				return err
			}
			if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
				return fmt.Errorf("mapping keys must be strings")
			}
			if _, exists := keys[key.Value]; exists {
				return fmt.Errorf("duplicate mapping key %q", key.Value)
			}
			keys[key.Value] = struct{}{}
			if err := validateYAMLStructure(node.Content[index+1], visited, depth+1, nodeCount); err != nil {
				return err
			}
		}
		return nil

	case yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!null", "!!bool", "!!str":
			return nil
		case "!!int":
			if !decimalIntegerPattern.MatchString(node.Value) {
				return fmt.Errorf("YAML integers must use decimal JSON syntax")
			}
			return nil
		case "!!float":
			return fmt.Errorf("YAML floating-point values are not supported")
		case "!!timestamp":
			return fmt.Errorf("YAML timestamps must be quoted strings")
		default:
			return fmt.Errorf("unsupported YAML tag %q", node.ShortTag())
		}

	default:
		return fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
}

var decimalIntegerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, string:
		return typed, nil
	case int:
		return json.Number(strconv.FormatInt(int64(typed), 10)), nil
	case int8:
		return json.Number(strconv.FormatInt(int64(typed), 10)), nil
	case int16:
		return json.Number(strconv.FormatInt(int64(typed), 10)), nil
	case int32:
		return json.Number(strconv.FormatInt(int64(typed), 10)), nil
	case int64:
		return json.Number(strconv.FormatInt(typed, 10)), nil
	case uint:
		return json.Number(strconv.FormatUint(uint64(typed), 10)), nil
	case uint8:
		return json.Number(strconv.FormatUint(uint64(typed), 10)), nil
	case uint16:
		return json.Number(strconv.FormatUint(uint64(typed), 10)), nil
	case uint32:
		return json.Number(strconv.FormatUint(uint64(typed), 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(typed, 10)), nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported YAML value type %T", value)
	}
}
