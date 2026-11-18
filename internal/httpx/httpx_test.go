package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func logger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestURLMergesSessionParams(t *testing.T) {
	c := New(Options{BaseURL: "http://h:1/base/", SessionParams: P("pageSize", "1000", "apikey", "k")})
	got := c.URL("/api/v3/queue", P("pageSize", "250", "includeSeries", "True", "q", "a b&c"))
	want := "http://h:1/base/api/v3/queue?apikey=k&pageSize=250&includeSeries=True&q=a+b%26c"
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
	if got := c.URL("/x?cmd=1", nil); got != "http://h:1/base/x?cmd=1&pageSize=1000&apikey=k" {
		t.Errorf("got %s", got)
	}
	c.SetSessionParams(nil)
	if got := c.URL("/x", nil); got != "http://h:1/base/x" {
		t.Errorf("got %s", got)
	}
}

func TestGetStatusesAndLogging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key" || r.Header.Get("User-Agent") != UserAgent {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`[{"a": 1}]`))
		case "/empty":
			_, _ = w.Write([]byte(`[]`))
		case "/html":
			_, _ = w.Write([]byte(`<html>`))
		case "/site":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"meta":{"msg":"api.err.NoSiteContext"}}`))
		case "/login":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`api.err.LoginRequired`))
		case "/boom":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	var buf bytes.Buffer
	c := New(Options{BaseURL: srv.URL, Headers: map[string]string{"X-Api-Key": "key"}, Log: logger(&buf)})
	ctx := context.Background()

	if body, ok := c.Get(ctx, "/ok", nil); !ok || string(body) != `[{"a": 1}]` {
		t.Errorf("ok: %q %v", body, ok)
	}
	var out []map[string]int
	if !c.GetInto(ctx, "/ok", nil, &out) || out[0]["a"] != 1 {
		t.Errorf("GetInto = %v", out)
	}
	if c.GetInto(ctx, "/empty", nil, &out) {
		t.Error("empty JSON is falsy, like Python's `if not get`")
	}
	var wrong struct{}
	if c.GetInto(ctx, "/ok", nil, &wrong) {
		t.Error("an array cannot decode into a struct")
	}
	checks := map[string]string{
		"/html":    "No JSON response. Response is: <html>",
		"/site":    "Your Site is incorrect for " + srv.URL + "/site",
		"/login":   "Your login credentials are incorrect for",
		"/boom":    "Unexpected HTTP 502 from",
		"/missing": "This url doesnt even resolve: " + srv.URL + "/missing",
	}
	for path, msg := range checks {
		buf.Reset()
		if _, ok := c.Get(ctx, path, nil); ok {
			t.Errorf("%s should fail", path)
		}
		if !strings.Contains(buf.String(), msg) {
			t.Errorf("%s: log %q lacks %q", path, buf.String(), msg)
		}
	}
	c.SetHeader("X-Api-Key", "")
	buf.Reset()
	if _, ok := c.Get(ctx, "/ok", nil); ok || !strings.Contains(buf.String(), "Your api key is incorrect") {
		t.Errorf("removed header: %s", buf.String())
	}
}

func TestDoPostsJSONAndKeepsCookies(t *testing.T) {
	var gotBody string
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			data, _ := io.ReadAll(r.Body)
			gotBody = string(data)
			http.SetCookie(w, &http.Cookie{Name: "unifises", Value: "abc", Path: "/"})
			_, _ = w.Write([]byte(`{}`))
		default:
			if ck, err := r.Cookie("unifises"); err == nil {
				gotCookie = ck.Value
			}
			_, _ = w.Write([]byte(`{"data": [1]}`))
		}
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, Cookies: true})
	resp, err := c.Do(context.Background(), http.MethodPost, "/api/login", nil,
		map[string]any{"username": "u", "remember": true})
	if err != nil || resp.Status != 200 || len(resp.Cookies) != 1 || resp.Cookies[0].Value != "abc" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	if gotBody != `{"remember":true,"username":"u"}` {
		t.Errorf("body = %s", gotBody)
	}
	var data struct{ Data []int }
	if err := resp.JSON(&struct{}{}); err != nil {
		t.Error(err)
	}
	if !c.GetInto(context.Background(), "/api/self", nil, &data) || gotCookie != "abc" {
		t.Errorf("cookie not sent: %q", gotCookie)
	}
	if _, err := c.Do(context.Background(), http.MethodPost, "/x", nil, func() {}); err == nil {
		t.Error("unencodable body should fail")
	}
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestTransportErrorMessages(t *testing.T) {
	cases := map[string]error{
		"Request timed out":              timeoutErr{},
		"Cannot resolve the url/ip/port": &net.OpError{Op: "dial", Err: errors.New("connection refused")},
		"Request cancelled":              context.Canceled,
	}
	for msg, err := range cases {
		var buf bytes.Buffer
		c := New(Options{BaseURL: "http://h", Log: logger(&buf), Transport: failingTransport{err}})
		if _, ok := c.Get(context.Background(), "/x", nil); ok {
			t.Error("should fail")
		}
		if !strings.Contains(buf.String(), msg) {
			t.Errorf("%v: log %q lacks %q", err, buf.String(), msg)
		}
	}
	var buf bytes.Buffer
	c := New(Options{BaseURL: "ftp://h", Log: logger(&buf)})
	if _, ok := c.Get(context.Background(), "/x", nil); ok || !strings.Contains(buf.String(), "http(s)://") {
		t.Errorf("scheme: %s", buf.String())
	}
	buf.Reset()
	c = New(Options{BaseURL: "http://h\x7f", Log: logger(&buf)})
	if _, ok := c.Get(context.Background(), "/x", nil); ok || !strings.Contains(buf.String(), "Invalid request URL") {
		t.Errorf("bad url: %s", buf.String())
	}
}

func TestTLSVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	if _, ok := New(Options{BaseURL: srv.URL, VerifySSL: true, Log: logger(&buf)}).Get(context.Background(), "/", nil); ok {
		t.Error("self-signed certificate accepted with verify_ssl")
	}
	if !strings.Contains(buf.String(), "SSL issue") {
		t.Errorf("log = %s", buf.String())
	}
	if _, ok := New(Options{BaseURL: srv.URL}).Get(context.Background(), "/", nil); !ok {
		t.Error("verify_ssl = false should accept the certificate")
	}
}

func TestTruthy(t *testing.T) {
	for body, want := range map[string]bool{`[]`: false, ` [ ] `: false, `{}`: false, `[0]`: true, `{"a":1}`: true,
		`0`: false, `1`: true, `""`: false, `"x"`: true, `null`: false, `false`: false, `true`: true, `x`: false} {
		if Truthy([]byte(body)) != want {
			t.Errorf("Truthy(%s) != %v", body, want)
		}
	}
}
