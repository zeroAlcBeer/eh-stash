package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CheerChen/eh-stash/scraper-go/config"
	"github.com/CheerChen/eh-stash/scraper-go/ratelimit"
	utls "github.com/refraction-networking/utls"
)

var banRE = regexp.MustCompile(
	`(?i)ban expires in\s+(?:(\d+)\s*hours?)?\s*,?\s*(?:(\d+)\s*minutes?)?\s*(?:and\s+)?(?:(\d+)\s*seconds?)?`,
)

// Client wraps an HTTP client with TLS fingerprinting, rate limiting, and ban detection.
type Client struct {
	http    *http.Client
	cfg     *config.Config
	limiter *ratelimit.Limiter
}

// New creates a Client with uTLS Chrome fingerprint and optional proxy.
func New(cfg *config.Config, limiter *ratelimit.Limiter) (*Client, error) {
	jar, _ := cookiejar.New(nil)

	// Set cookies for the base URL
	baseURL, err := url.Parse(cfg.ExBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	var cookies []*http.Cookie
	for k, v := range cfg.Cookies {
		cookies = append(cookies, &http.Cookie{Name: k, Value: v})
	}
	jar.SetCookies(baseURL, cookies)

	transport := newUTLSTransport(cfg.ProxyURL)

	httpClient := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		Transport: transport,
	}

	return &Client{
		http:    httpClient,
		cfg:     cfg,
		limiter: limiter,
	}, nil
}

// BaseURL returns the configured ExHentai base URL.
func (c *Client) BaseURL() string {
	return c.cfg.ExBaseURL
}

type FetchResult struct {
	Body       string
	StatusCode int
}

const (
	ResultOK     = "ok"
	ResultBanned = "banned"
	ResultError  = "error"
)

// FetchPage fetches a page with rate limiting and ban detection.
// Returns (body, resultType, error).
func (c *Client) FetchPage(ctx context.Context, pageURL string) (string, string, error) {
	if err := c.limiter.Acquire(ctx); err != nil {
		return "", ResultError, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", ResultError, err
	}
	for k, vals := range c.cfg.Headers {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", ResultError, fmt.Errorf("HTTP GET %s: %w", pageURL, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", ResultError, fmt.Errorf("read body: %w", err)
	}
	body := string(bodyBytes)

	// Ban detection
	if strings.Contains(body, "temporarily banned") || strings.Contains(body, "IP address has been") {
		secs := parseBanSeconds(body)
		c.limiter.SetBan(time.Duration(secs) * time.Second)
		return "", ResultBanned, nil
	}

	// Sad Panda / blank page
	if resp.StatusCode == 200 && len(body) < 100 && !strings.Contains(body, "<") {
		return "", ResultError, fmt.Errorf("sad panda or blank response")
	}

	if resp.StatusCode != 200 {
		return "", ResultError, fmt.Errorf("HTTP %d from %s", resp.StatusCode, pageURL)
	}

	return body, ResultOK, nil
}

// FetchThumb downloads a thumbnail image.
// Cookies are added explicitly because the thumbnail CDN domain (e.g. ehgt.org)
// differs from ExBaseURL, so the cookiejar won't send them automatically.
func (c *Client) FetchThumb(ctx context.Context, thumbURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", thumbURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Referer", c.cfg.ExBaseURL)
	req.Header.Set("User-Agent", c.cfg.Headers.Get("User-Agent"))

	// Explicitly attach cookies regardless of domain
	for k, v := range c.cfg.Cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func parseBanSeconds(text string) int {
	m := banRE.FindStringSubmatch(text)
	if m == nil {
		return 300 // fallback 5 minutes
	}
	hours, _ := strconv.Atoi(m[1])
	mins, _ := strconv.Atoi(m[2])
	secs, _ := strconv.Atoi(m[3])
	total := hours*3600 + mins*60 + secs
	if total <= 0 {
		return 300
	}
	return total
}

// ValidateAccess checks that the client can access ExHentai.
func (c *Client) ValidateAccess(ctx context.Context) error {
	body, result, err := c.FetchPage(ctx, c.cfg.ExBaseURL)
	if err != nil {
		return fmt.Errorf("validate access: %w", err)
	}
	if result == ResultBanned {
		return fmt.Errorf("IP is currently banned")
	}
	if strings.Contains(body, "Sad Panda") {
		return fmt.Errorf("sad panda - check cookies")
	}
	if !strings.Contains(body, "front_page") && !strings.Contains(body, "itg") {
		slog.Warn("unexpected response content, may need cookie refresh")
	}
	return nil
}

// newUTLSTransport creates an http.Transport that uses uTLS for Chrome TLS fingerprinting.
func newUTLSTransport(proxyURL string) http.RoundTripper {
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	return &utlsTransport{
		dialer:   dialer,
		proxyURL: proxyURL,
	}
}

type utlsTransport struct {
	dialer   *net.Dialer
	proxyURL string
}

func (t *utlsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		if req.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addr := net.JoinHostPort(host, port)

	if req.URL.Scheme != "https" {
		// Plain HTTP — use standard transport
		return http.DefaultTransport.RoundTrip(req)
	}

	// Dial TCP
	var rawConn net.Conn
	var err error

	if t.proxyURL != "" {
		rawConn, err = dialViaProxy(t.proxyURL, addr, t.dialer)
	} else {
		rawConn, err = t.dialer.DialContext(req.Context(), "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	// uTLS handshake with Chrome fingerprint, force HTTP/1.1 to avoid h2 framing issues
	tlsConn := utls.UClient(rawConn, &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
		NextProtos:         []string{"http/1.1"},
	}, utls.HelloCustom)

	// Apply Chrome-like fingerprint spec but with HTTP/1.1 ALPN
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("get Chrome spec: %w", err)
	}
	// Override ALPN to HTTP/1.1 only
	for i, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
			spec.Extensions[i] = alpn
		}
	}
	if err := tlsConn.ApplyPreset(&spec); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("apply TLS spec: %w", err)
	}

	if err := tlsConn.Handshake(); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("TLS handshake: %w", err)
	}

	// Use the TLS connection with a single-use transport
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tlsConn, nil
		},
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
	}

	return tr.RoundTrip(req)
}

func dialViaProxy(proxyURLStr, target string, dialer *net.Dialer) (net.Conn, error) {
	proxyParsed, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}

	conn, err := dialer.Dial("tcp", proxyParsed.Host)
	if err != nil {
		return nil, fmt.Errorf("dial proxy: %w", err)
	}

	// CONNECT for HTTPS tunneling
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if proxyParsed.User != nil {
		// Basic auth not implemented here for simplicity
		// Could add if needed
	}
	connectReq += "\r\n"

	_, err = conn.Write([]byte(connectReq))
	if err != nil {
		conn.Close()
		return nil, err
	}

	// Read response (simple)
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp := string(buf[:n])
	if !strings.Contains(resp, "200") {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp)
	}

	return conn, nil
}
