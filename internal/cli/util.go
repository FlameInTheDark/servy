package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/FlameInTheDark/servy/internal/safehttp"
)

func jsonUnmarshal(data []byte, v interface{}) error { return json.Unmarshal(data, v) }

func fetchURL(url string) ([]byte, error) {
	client := safehttp.NewClient(30 * time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("download exceeds the 8 MiB limit")
	}
	return data, nil
}
