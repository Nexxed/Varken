package influx

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Settings are the [influxdb] options.
type Settings struct {
	URL       string // host (and optional path), without scheme, as in varken.ini
	Port      int
	SSL       bool
	VerifySSL bool
	Username  string
	Password  string
	Database  string // "varken" unless configured
}

const (
	// RetentionPolicy is the policy Varken creates with a new database.
	RetentionPolicy = "varken 30d-1h"
	requestTimeout  = 30 * time.Second
)

// Client talks to the InfluxDB 1.x HTTP API.
type Client struct {
	base     *url.URL
	http     *http.Client
	username string
	password string
	database string
	log      *slog.Logger
}

// NewClient builds a client. Like the Python client, credentials are sent as basic auth.
func NewClient(s Settings, log *slog.Logger) (*Client, error) {
	scheme := "http"
	if s.SSL {
		scheme = "https"
	}
	host, path, _ := strings.Cut(s.URL, "/")
	if host == "" {
		return nil, errors.New("influxdb url is empty")
	}
	base, err := url.Parse(fmt.Sprintf("%s://%s/%s", scheme, net.JoinHostPort(host, strconv.Itoa(s.Port)), path))
	if err != nil {
		return nil, fmt.Errorf("influxdb url: %w", err)
	}
	base.Path = strings.TrimSuffix(base.Path, "/")
	database := s.Database
	if database == "" {
		database = "varken"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: !s.VerifySSL} //nolint:gosec // user setting verify_ssl
	return &Client{
		base:     base,
		http:     &http.Client{Transport: transport, Timeout: requestTimeout},
		username: s.Username,
		password: s.Password,
		database: database,
		log:      log,
	}, nil
}

// Database is the database written to.
func (c *Client) Database() string { return c.database }

func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path += path
	u.RawQuery = query.Encode()
	return u.String()
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte,
	headers map[string]string) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), reader)
	if err != nil {
		return nil, nil, err
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp, data, err
}

// Ping checks the server and returns its version.
func (c *Client) Ping(ctx context.Context) (string, error) {
	resp, _, err := c.do(ctx, http.MethodGet, "/ping", nil, nil, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("ping returned HTTP %d", resp.StatusCode)
	}
	return resp.Header.Get("X-Influxdb-Version"), nil
}

type queryResponse struct {
	Results []struct {
		Error  string `json:"error"`
		Series []struct {
			Columns []string `json:"columns"`
			Values  [][]any  `json:"values"`
		} `json:"series"`
	} `json:"results"`
	Error string `json:"error"`
}

// Query runs an InfluxQL statement and returns its rows as maps of column to value.
func (c *Client) Query(ctx context.Context, method, q string) ([]map[string]any, error) {
	query := url.Values{"q": {q}, "db": {c.database}}
	resp, body, err := c.do(ctx, method, "/query", query, nil, nil)
	if err != nil {
		return nil, err
	}
	var parsed queryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("query %q: HTTP %d: %s", q, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK || parsed.Error != "" {
		return nil, fmt.Errorf("query %q: HTTP %d: %s", q, resp.StatusCode, parsed.Error)
	}
	var rows []map[string]any
	for _, result := range parsed.Results {
		if result.Error != "" {
			return nil, fmt.Errorf("query %q: %s", q, result.Error)
		}
		for _, series := range result.Series {
			for _, values := range series.Values {
				row := map[string]any{}
				for i, col := range series.Columns {
					if i < len(values) {
						row[col] = values[i]
					}
				}
				rows = append(rows, row)
			}
		}
	}
	return rows, nil
}

func quoteIdent(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// EnsureDatabase creates the database, with Varken's 30-day default retention policy, when it does not exist.
// An existing database is left as it is, as before.
func (c *Client) EnsureDatabase(ctx context.Context) error {
	rows, err := c.Query(ctx, http.MethodGet, "SHOW DATABASES")
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row["name"] == c.database {
			return nil
		}
	}
	c.log.Info(fmt.Sprintf("Creating %s database", c.database))
	if _, err := c.Query(ctx, http.MethodPost, "CREATE DATABASE "+quoteIdent(c.database)); err != nil {
		return err
	}
	policies, err := c.retentionPolicies(ctx)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if p["name"] == RetentionPolicy {
			return nil
		}
	}
	c.log.Info("Creating varken retention policy (30d-1h)")
	_, err = c.Query(ctx, http.MethodPost, fmt.Sprintf(
		"CREATE RETENTION POLICY %s ON %s DURATION 30d REPLICATION 1 SHARD DURATION 1h DEFAULT",
		quoteIdent(RetentionPolicy), quoteIdent(c.database)))
	return err
}

func (c *Client) retentionPolicies(ctx context.Context) ([]map[string]any, error) {
	return c.Query(ctx, http.MethodGet, "SHOW RETENTION POLICIES ON "+quoteIdent(c.database))
}

// DefaultRetention returns how long the database's default retention policy keeps data; 0 means forever.
func (c *Client) DefaultRetention(ctx context.Context) (time.Duration, error) {
	policies, err := c.retentionPolicies(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range policies {
		if isDefault, _ := p["default"].(bool); isDefault {
			text, _ := p["duration"].(string)
			if text == "" || text == "0" || text == "0s" {
				return 0, nil
			}
			return time.ParseDuration(text)
		}
	}
	return 0, nil
}

// WriteError is a write InfluxDB answered with an error status.
type WriteError struct {
	Status int
	Body   string
}

func (e *WriteError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

// Retryable reports a server-side failure worth retrying.
func (e *WriteError) Retryable() bool { return e.Status >= 500 }

// BeyondRetention reports InfluxDB's partial write for back-dated points: the rest were written.
func (e *WriteError) BeyondRetention() bool {
	return strings.Contains(e.Body, "beyond retention policy")
}

// WriteBody posts line protocol to /write, gzipped when that saves space.
func (c *Client) WriteBody(ctx context.Context, body []byte) error {
	headers := map[string]string{"Content-Type": "application/octet-stream"}
	if len(body) > 1024 {
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		_, _ = w.Write(body)
		if err := w.Close(); err == nil {
			body = gz.Bytes()
			headers["Content-Encoding"] = "gzip"
		}
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/write", url.Values{"db": {c.database}}, body, headers)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		var parsed struct {
			Error string `json:"error"`
		}
		text := strings.TrimSpace(string(data))
		if json.Unmarshal(data, &parsed) == nil && parsed.Error != "" {
			text = parsed.Error
		}
		return &WriteError{Status: resp.StatusCode, Body: text}
	}
	return nil
}
