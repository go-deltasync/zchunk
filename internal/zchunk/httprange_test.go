package zchunk

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// roundTripFunc lets a function act as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// errBody is a response body whose Read always fails.
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errBoom }
func (errBody) Close() error             { return nil }

func clientWith(f roundTripFunc) *http.Client { return &http.Client{Transport: f} }

func TestHTTPRangeReaderSuccess(t *testing.T) {
	const full = "0123456789ABCDEFGHIJ" // 20 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f", time.Time{}, strings.NewReader(full))
	}))
	defer srv.Close()

	// Body begins at offset 4; request bytes [4+2, 4+2+3) = "678".
	rr := NewHTTPRangeReader(srv.URL, 4, nil)
	got, err := rr.ReadRange(2, 3)
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if string(got) != "678" {
		t.Fatalf("ReadRange = %q, want %q", got, "678")
	}
}

func TestHTTPRangeReaderBuildRequestError(t *testing.T) {
	// A control character in the URL makes http.NewRequest fail.
	rr := NewHTTPRangeReader("http://example.com/\x7f", 0, clientWith(nil))
	if _, err := rr.ReadRange(0, 1); err == nil {
		t.Fatal("expected build-request error")
	}
}

func TestHTTPRangeReaderDoError(t *testing.T) {
	rr := NewHTTPRangeReader("http://example.invalid/x", 0, clientWith(
		func(*http.Request) (*http.Response, error) { return nil, errBoom },
	))
	if _, err := rr.ReadRange(0, 1); err == nil {
		t.Fatal("expected client.Do error")
	}
}

func TestHTTPRangeReaderBadStatus(t *testing.T) {
	rr := NewHTTPRangeReader("http://x/y", 0, clientWith(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, // not 206
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("whole file")),
		}, nil
	}))
	if _, err := rr.ReadRange(0, 1); err == nil {
		t.Fatal("expected non-206 status error")
	}
}

func TestHTTPRangeReaderBodyError(t *testing.T) {
	rr := NewHTTPRangeReader("http://x/y", 0, clientWith(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Status:     "206 Partial Content",
			Body:       errBody{},
		}, nil
	}))
	if _, err := rr.ReadRange(0, 1); err == nil {
		t.Fatal("expected body read error")
	}
}
