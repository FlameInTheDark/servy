package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func jsonUnmarshal(data []byte, v interface{}) error { return json.Unmarshal(data, v) }

func fetchURL(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
