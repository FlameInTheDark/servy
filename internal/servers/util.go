package servers

import "encoding/json"

// jsonUnmarshal is a tiny indirection (keeps import surface tidy).
func jsonUnmarshal(data []byte, v interface{}) error { return json.Unmarshal(data, v) }

// parseJSONStrings decodes a nullable JSON string-array column
// (docker_ports / docker_binds); missing/empty → nil.
func parseJSONStrings(src *string) []string {
	if src == nil || *src == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*src), &out); err != nil {
		return nil
	}
	return out
}

// parseJSONMap decodes a nullable JSON object column (docker_env).
func parseJSONMap(src *string) map[string]string {
	if src == nil || *src == "" {
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(*src), &out); err != nil {
		return nil
	}
	return out
}
