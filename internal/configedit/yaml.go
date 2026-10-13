package configedit

import (
	"gopkg.in/yaml.v3"
)

// ParseYAML validates YAML bytes into a generic tree.
func ParseYAML(data []byte) (interface{}, error) {
	var v interface{}
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// FormatYAML re-serializes a YAML document.
func FormatYAML(data []byte) ([]byte, error) {
	var v interface{}
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return yaml.Marshal(v)
}
