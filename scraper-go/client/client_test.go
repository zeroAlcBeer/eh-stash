package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zeroAlcBeer/eh-stash/scraper-go/config"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/ratelimit"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchPageClassifiesMissingGallery(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := &Client{
				http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: status,
						Body:       io.NopCloser(strings.NewReader("missing")),
						Header:     make(http.Header),
					}, nil
				})},
				cfg:     &config.Config{ExBaseURL: "https://example.test"},
				limiter: ratelimit.New(0, 0),
			}
			body, result, err := c.FetchPage(context.Background(), "https://example.test/g/1/token/")
			if err != nil {
				t.Fatalf("FetchPage returned error: %v", err)
			}
			if result != ResultNotFound || body != "" {
				t.Fatalf("result=%q body=%q, want %q and empty body", result, body, ResultNotFound)
			}
		})
	}
}
