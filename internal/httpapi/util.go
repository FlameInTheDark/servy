package httpapi

import (
	"encoding/json"
	"time"
)

func jsonMarshal(v interface{}) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v interface{}) error { return json.Unmarshal(b, v) }
func databaseNow() string                         { return time.Now().UTC().Format(time.RFC3339) }
func b2l(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
