package configedit

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/FlameInTheDark/servy/internal/safehttp"
)

// httpClientTimeout bounds manifest fetches and rejects unsafe destinations.
var httpClientTimeout = safehttp.NewClient(20 * time.Second)

// readAllLimit reads at most n bytes.
func readAllLimit(r io.Reader, n int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, n+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > n {
		return nil, errors.New("response exceeds the manifest size limit")
	}
	return data, nil
}
