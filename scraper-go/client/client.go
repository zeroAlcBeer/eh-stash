package client

import (
	"context"
	"crypto/tls"
	"errors"
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

	utls "github.com/refraction-networking/utls"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/config"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/egress"
	"github.com/zeroAlcBeer/eh-stash/scraper-go/ratelimit"
)

var banRE = regexp.MustCompile(
	`(?i)ban expires in\s+(?:(\d+)\s*hours?)?\s*,?\s*(?:(\d+)\s*minutes?)?\s*(?:and\s+)?(?:(\d+)\s*seconds?)?`,
)

// BanEventFunc is called when a ban is detected, so the caller can
// persist the event (e.g. write to sync_task_events for the API to pick up).
type BanEventFunc func(durationSecs int, pageURL string)

// Client wraps an HTTP client with TLS fingerprinting, rate limiting, and ban detection.
//
// http      — uTLS-fingerprinted, used for exhentai.org main site (anti-CF).
// thumbHTTP — standard TLS, keep-alive enabled, used for the s.exhentai.org
//
//	CDN. Thumbs don't need fingerprint mimicry and the CDN throttles
//	concurrent TLS handshakes; one persistent connection per process
//	amortizes the handshake cost to ~0.
type Client struct {
	http      *http.Client
	thumbHTTP *http.Client
	cfg       *config.Config
	egress    *egress.Manager
	limiter   *ratelimit.Limiter
	onBan     BanEventFunc
}

// SetBanEventHandler registers a callback invoked when a ban is detected.
func (c *Client) SetBanEventHandler(f BanEventFunc) {
	c.onBan = f
}

// New creates a Client with uTLS Chrome fingerprint and optional proxy.
func New(cfg *config.Config, limiter *ratelimit.Limiter, egressMgr *egress.Manager) (*Client, error) {
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

	transport := newUTLSTransport(func() string {
		if egressMgr == nil {
			return cfg.ProxyURL
		}
		return egressMgr.CurrentProxyURL()
	})

	httpClient := &http.Client{
		Jar:       jar,
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	// Separate client for thumb downloads: standard transport with keep-alive
	// + per-step timeouts. Bench showed 6x speedup vs the one-shot uTLS path
	// (p50 1184ms -> 307ms) and eliminates the "N concurrent handshakes get
	// throttled by s.exhentai.org" failure mode.
	thumbHTTP := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}

	return &Client{
		http:      httpClient,
		thumbHTTP: thumbHTTP,
		cfg:       cfg,
		egress:    egressMgr,
		limiter:   limiter,
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
	ResultOK       = "ok"
	ResultBanned   = "banned"
	ResultNotFound = "not_found"
	ResultError    = "error"
)

// FetchPage fetches a page with rate limiting and ban detection.
// Returns (body, resultType, error).
func (c *Client) FetchPage(ctx context.Context, pageURL string) (string, string, error) {
	mode := c.currentMode()
	body, result, err := c.fetchPageWithMode(ctx, pageURL, mode, true)
	return body, result, err
}

func (c *Client) ProbeAccess(ctx context.Context, mode egress.Mode) error {
	body, result, err := c.fetchPageWithMode(ctx, c.cfg.ExBaseURL, mode, false)
	if err != nil {
		return err
	}
	if result == ResultBanned {
		return fmt.Errorf("IP is currently banned")
	}
	if strings.Contains(body, "Sad Panda") {
		return fmt.Errorf("sad panda - check cookies")
	}
	if !strings.Contains(body, "front_page") && !strings.Contains(body, "itg") {
		return fmt.Errorf("unexpected probe response")
	}
	return nil
}

// BanProbe checks whether the ban is still active without going through
// the rate limiter (to avoid deadlock when called from waitBan).
// Returns stillBanned=true if the ban page is served, false if the site
// responds normally, or an error if the probe was inconclusive.
func (c *Client) BanProbe(ctx context.Context) (bool, error) {
	mode := c.currentMode()

	req, err := http.NewRequestWithContext(ctx, "GET", c.cfg.ExBaseURL, nil)
	if err != nil {
		return false, err
	}
	for k, vals := range c.cfg.Headers {
		for _, v := range vals {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.do(req, mode)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	body := string(bodyBytes)

	if strings.Contains(body, "temporarily banned") || strings.Contains(body, "IP address has been") {
		return true, nil
	}
	if resp.StatusCode == 200 && len(body) > 100 {
		return false, nil
	}
	return false, fmt.Errorf("inconclusive probe: status=%d len=%d", resp.StatusCode, len(body))
}

func (c *Client) fetchPageWithMode(ctx context.Context, pageURL string, mode egress.Mode, report bool) (string, string, error) {
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

	resp, err := c.do(req, mode)
	if err != nil {
		wrapped := fmt.Errorf("HTTP GET %s: %w", pageURL, err)
		if report {
			c.reportFailure(mode, classifyErr(wrapped), wrapped)
		}
		return "", ResultError, wrapped
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
		if c.onBan != nil {
			c.onBan(secs, pageURL)
		}
		if report {
			c.reportFailure(mode, egress.ErrKindBan, nil)
		}
		return "", ResultBanned, nil
	}

	// Sad Panda / blank page
	if resp.StatusCode == 200 && len(body) < 100 && !strings.Contains(body, "<") {
		err := fmt.Errorf("sad panda or blank response")
		if report {
			c.reportFailure(mode, egress.ErrKindParse, err)
		}
		return "", ResultError, err
	}

	// A removed detail page is a content state, not an egress failure. Callers
	// that reconcile stored galleries can mark the row inactive instead of
	// retrying it forever or rotating a healthy proxy.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		if report {
			c.reportSuccess(mode)
		}
		return "", ResultNotFound, nil
	}

	if resp.StatusCode != 200 {
		err := fmt.Errorf("HTTP %d from %s", resp.StatusCode, pageURL)
		if report {
			kind := egress.ErrKindHTTPStatus
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
				kind = egress.ErrKindAuth
			}
			c.reportFailure(mode, kind, err)
		}
		return "", ResultError, err
	}

	if report {
		c.reportSuccess(mode)
	}
	return body, ResultOK, nil
}

// FetchThumb downloads a thumbnail image.
// Uses the standard-TLS, keep-alive thumbHTTP client. Cookies are added
// explicitly because the thumbnail CDN domain (e.g. s.exhentai.org) differs
// from ExBaseURL, so the cookiejar won't send them automatically.
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

	resp, err := c.thumbHTTP.Do(req)
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
	body, result, err := c.fetchPageWithMode(ctx, c.cfg.ExBaseURL, c.currentMode(), false)
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
func newUTLSTransport(proxyURL func() string) http.RoundTripper {
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	return &utlsTransport{
		dialer:   dialer,
		proxyURL: proxyURL,
	}
}

type utlsTransport struct {
	dialer   *net.Dialer
	proxyURL func() string
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

	proxyURL := ""
	if t.proxyURL != nil {
		proxyURL = t.proxyURL()
	}
	if proxyURL != "" {
		rawConn, err = dialViaProxy(proxyURL, addr, t.dialer)
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

	// HandshakeContext (not Handshake) so a cancelled req.Context() — including
	// http.Client.Timeout and our per-fetch ctx in the thumb worker — actually
	// aborts a hung TLS handshake instead of leaking a goroutine in IO wait.
	if err := tlsConn.HandshakeContext(req.Context()); err != nil {
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

func (c *Client) currentMode() egress.Mode {
	if c.egress == nil {
		if c.cfg.ProxyURL == "" {
			return egress.ModeDirect
		}
		return egress.ModeProxy
	}
	return c.egress.CurrentMode()
}

func (c *Client) do(req *http.Request, mode egress.Mode) (*http.Response, error) {
	if mode == c.currentMode() {
		return c.http.Do(req)
	}

	clone := req.Clone(req.Context())
	tr := newUTLSTransport(func() string {
		if mode == egress.ModeProxy {
			return c.cfg.ProxyURL
		}
		return ""
	})
	client := &http.Client{
		Jar:       c.http.Jar,
		Timeout:   c.http.Timeout,
		Transport: tr,
	}
	return client.Do(clone)
}

func (c *Client) reportSuccess(mode egress.Mode) {
	if c.egress != nil {
		c.egress.ReportSuccess(mode)
	}
}

func (c *Client) reportFailure(mode egress.Mode, kind egress.ErrKind, err error) {
	if c.egress != nil {
		c.egress.ReportFailure(mode, kind, err)
	}
}

func classifyErr(err error) egress.ErrKind {
	if err == nil {
		return egress.ErrKindNone
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return egress.ErrKindTimeout
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "proxy connect failed"), strings.Contains(msg, "dial proxy"), strings.Contains(msg, "connect failed"):
		return egress.ErrKindProxyConnect
	case strings.Contains(msg, "tls handshake"), strings.Contains(msg, "ssl"), strings.Contains(msg, "x509"), strings.Contains(msg, "eof"):
		return egress.ErrKindTLSHandshake
	case strings.Contains(msg, "timeout"):
		return egress.ErrKindTimeout
	default:
		return egress.ErrKindParse
	}
}
