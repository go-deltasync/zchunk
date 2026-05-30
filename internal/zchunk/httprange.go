package zchunk

import (
	"fmt"
	"io"
	"net/http"
)

// HTTPRangeReader is a RangeReader that fetches byte ranges of a remote zchunk
// file over HTTP range requests. Offsets passed to ReadRange are relative to the
// body, so Base is the absolute file offset at which the body begins (the file's
// data offset: lead + preface + index + signatures).
type HTTPRangeReader struct {
	URL    string
	Base   int64
	Client *http.Client
}

// NewHTTPRangeReader returns a RangeReader for url whose body begins at base
// bytes into the file. If client is nil, http.DefaultClient is used.
func NewHTTPRangeReader(url string, base int64, client *http.Client) *HTTPRangeReader {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPRangeReader{URL: url, Base: base, Client: client}
}

// ReadRange fetches length bytes starting at offset within the body via an HTTP
// range request, requiring a 206 Partial Content response.
func (h *HTTPRangeReader) ReadRange(offset, length int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, h.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("zchunk: build range request: %w", err)
	}
	start := h.Base + offset
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))

	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zchunk: http range request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("zchunk: http range request: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("zchunk: read range body: %w", err)
	}
	return data, nil
}
