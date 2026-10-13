package configedit

import (
	"io"
	"net/http"
	"time"
)

// httpClientTimeout bounds manifest fetches.
var httpClientTimeout = http.Client{Timeout: 20 * time.Second}

// readAllLimit reads at most n bytes.
func readAllLimit(r io.Reader, n int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, n))
}
