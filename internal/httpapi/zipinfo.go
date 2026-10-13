package httpapi

import (
	"archive/zip"
	"bytes"
	"fmt"
)

// zipInfoReader wraps a zip.Reader over in-memory bytes (no lock copies).
type zipInfoReader struct{ r *zip.Reader }

func zipReaderBytes(data []byte) (*zipInfoReader, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	return &zipInfoReader{r: zr}, nil
}

func (z *zipInfoReader) Close() error { return nil }

func (z *zipInfoReader) Files() []*zip.File { return z.r.File }

func sscanf64(s string, v *int64) (int, error) {
	n, err := fmt.Sscan(s, v)
	return n, err
}
