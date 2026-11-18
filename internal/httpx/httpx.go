// Package httpx is the HTTP client the collectors share. It reproduces the Python connection_handler: the same
// timeouts, the same log messages for each kind of failure, and "no JSON" as the single failure signal, so a
// collector can simply stop when a request does not give it data.
package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	py "github.com/Nexxed/Varken/internal/pycompat"
)

const (
	// ConnectTimeout and ReadTimeout are the Python REQUEST_TIMEOUT (10, 60).
	ConnectTimeout = 10 * time.Second
	ReadTimeout    = 60 * time.Second
	maxBody        = 256 << 20
)

// UserAgent is sent with every request.
var UserAgent = "Varken"

// Param is one query parameter. Values are written exactly as given; pass the text Python's requests library
// would have sent (it sent True/False for booleans).
type Param struct{ Key, Value string }

// Params is an ordered query string.
type Params []Param

// P builds Params from alternating keys and values.
func P(kv ...string) Params {
	out := make(Params, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, Param{kv[i], kv[i+1]})
	}
	return out
}

// Options configure a Client.
type Options struct {
	BaseURL   string
	VerifySSL bool
	Headers   map[string]string
	// SessionParams are added to every request unless the request sets the same key (requests.Session.params).
	SessionParams Params
	Cookies       bool
	Log           *slog.Logger
	// Transport overrides the HTTP transport (tests).
	Transport http.RoundTripper
}

// Client is one upstream server's connection, like a requests.Session.
type Client struct {
	base    string
	http    *http.Client
	headers http.Header
	params  Params
	log     *slog.Logger
}

// New returns a client for one server.
func New(o Options) *Client {
	transport := o.Transport
	if transport == nil {
		t := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   ConnectTimeout,
			ResponseHeaderTimeout: ReadTimeout,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   4,
			ForceAttemptHTTP2:     true,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: !o.VerifySSL}, //nolint:gosec // verify_ssl setting
		}
		transport = t
	}
	client := &http.Client{Transport: transport, Timeout: 2 * ReadTimeout}
	if o.Cookies {
		client.Jar, _ = cookiejar.New(nil)
	}
	headers := http.Header{}
	headers.Set("User-Agent", UserAgent)
	headers.Set("Accept", "*/*")
	for k, v := range o.Headers {
		headers.Set(k, v)
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{base: strings.TrimRight(o.BaseURL, "/"), http: client, headers: headers, params: o.SessionParams,
		log: log.With("module", "helpers")}
}

// SetHeader changes a session header ("" removes it).
func (c *Client) SetHeader(key, value string) {
	if value == "" {
		c.headers.Del(key)
		return
	}
	c.headers.Set(key, value)
}

// SetSessionParams replaces the session parameters.
func (c *Client) SetSessionParams(p Params) { c.params = p }

// Response is a completed request.
type Response struct {
	Status  int
	Body    []byte
	Header  http.Header
	Cookies []*http.Cookie
	URL     string
}

// JSON decodes the body into out.
func (r *Response) JSON(out any) error { return json.Unmarshal(r.Body, out) }

// URL builds the full request URL with session and request parameters merged.
func (c *Client) URL(path string, params Params) string {
	merged := make(Params, 0, len(c.params)+len(params))
	for _, sp := range c.params {
		overridden := false
		for _, p := range params {
			if p.Key == sp.Key {
				overridden = true
				break
			}
		}
		if !overridden {
			merged = append(merged, sp)
		}
	}
	merged = append(merged, params...)
	u := c.base + path
	if len(merged) > 0 {
		parts := make([]string, len(merged))
		for i, p := range merged {
			parts[i] = url.QueryEscape(p.Key) + "=" + url.QueryEscape(p.Value)
		}
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + strings.Join(parts, "&")
	}
	return u
}

// Do sends a request and returns the response whatever its status (Python's as_is_reply). Transport failures
// are logged with the Python messages and returned as an error.
func (c *Client) Do(ctx context.Context, method, path string, params Params, jsonBody any) (*Response, error) {
	target := c.URL(path, params)
	var body io.Reader
	if jsonBody != nil {
		data, err := json.Marshal(jsonBody)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		c.log.Error(fmt.Sprintf("Invalid request URL %s: %v", target, err))
		return nil, err
	}
	for k, v := range c.headers {
		req.Header[k] = v
	}
	if jsonBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.logTransportError(err)
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		c.log.Error(fmt.Sprintf("Broken connection during request... oops? Error: %v", err))
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Body: data, Header: resp.Header, Cookies: resp.Cookies(),
		URL: target}, nil
}

func (c *Client) logTransportError(err error) {
	var urlErr *url.Error
	var tlsErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		c.log.Debug(fmt.Sprintf("Request cancelled: %v", err))
	case errors.As(err, &tlsErr) || errors.As(err, &recordErr):
		c.log.Error(fmt.Sprintf("Either your host is unreachable or you have an SSL issue. : %v", err))
	case errors.As(err, &netErr) && netErr.Timeout():
		c.log.Error(fmt.Sprintf("Request timed out after %s seconds: %v", "(10, 60)", err))
	case errors.As(err, &urlErr) && strings.Contains(urlErr.Err.Error(), "unsupported protocol scheme"):
		c.log.Error("You added http(s):// in the config file. Don't do that.")
	default:
		c.log.Error(fmt.Sprintf("Cannot resolve the url/ip/port. Check connectivity. Error: %v", err))
	}
}

// LogStatus logs a non-200 response the way connection_handler did. Statuses Python ignored silently are
// logged too, as warnings.
func (c *Client) LogStatus(r *Response) {
	switch r.Status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		switch {
		case bytes.Contains(r.Body, []byte("NoSiteContext")):
			c.log.Info("Your Site is incorrect for " + r.URL)
		case bytes.Contains(r.Body, []byte("LoginRequired")):
			c.log.Info("Your login credentials are incorrect for " + r.URL)
		default:
			c.log.Info("Your api key is incorrect for " + r.URL)
		}
	case http.StatusNotFound:
		c.log.Info("This url doesnt even resolve: " + r.URL)
	default:
		c.log.Warn(fmt.Sprintf("Unexpected HTTP %d from %s", r.Status, r.URL))
	}
}

// Get is connection_handler: it returns the JSON body of a 200 response, or ok=false after logging why not.
func (c *Client) Get(ctx context.Context, path string, params Params) (body []byte, ok bool) {
	return c.request(ctx, http.MethodGet, path, params)
}

func (c *Client) request(ctx context.Context, method, path string, params Params) ([]byte, bool) {
	resp, err := c.Do(ctx, method, path, params, nil)
	if err != nil {
		return nil, false
	}
	c.LogStatus(resp)
	if resp.Status != http.StatusOK {
		return nil, false
	}
	if !json.Valid(resp.Body) {
		c.log.Error("No JSON response. Response is: " + py.Truncate(string(resp.Body), 2000))
		return nil, false
	}
	return resp.Body, true
}

// GetInto fetches JSON and decodes it into out. It returns false when the request failed or, like Python's
// `if not get: return`, when the JSON is empty ({}, [], "", 0, false or null).
func (c *Client) GetInto(ctx context.Context, path string, params Params, out any) bool {
	body, ok := c.Get(ctx, path, params)
	if !ok || !Truthy(body) {
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		c.log.Error(fmt.Sprintf("Unexpected JSON from %s: %v", path, err))
		return false
	}
	return true
}

// Truthy is Python's bool() of the decoded JSON.
func Truthy(body []byte) bool {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && (body[0] == '[' || body[0] == '{') {
		// Avoid decoding a large array or object twice: it is truthy unless it closes straight away.
		rest := bytes.TrimSpace(body[1:])
		return len(rest) > 0 && rest[0] != ']' && rest[0] != '}'
	}
	var v py.Value
	if err := json.Unmarshal(body, &v); err != nil {
		return false
	}
	return v.Truthy()
}
