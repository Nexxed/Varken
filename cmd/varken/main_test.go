package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &errOut); code != 0 ||
		!strings.HasPrefix(out.String(), "Varken v") {
		t.Errorf("version: %d %q", code, out.String())
	}
	if code := run(context.Background(), []string{"bogus"}, &out, &errOut); code != 2 ||
		!strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("bogus: %d %q", code, errOut.String())
	}
	for _, args := range [][]string{{"-nope"}, {"import-tautulli", "-D", "x"}, {"grafana-build", "-nope"}} {
		errOut.Reset()
		if code := run(context.Background(), args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage") {
			t.Errorf("%v: %d %q", args, code, errOut.String())
		}
	}
}

func TestRunWithMissingFolder(t *testing.T) {
	t.Setenv("DATA_FOLDER", t.TempDir()+"/missing")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"-ND"}, &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "does not exist") {
		t.Errorf("run: %d %q", code, errOut.String())
	}
	if code := run(context.Background(), []string{"import-tautulli", "--days", "3"}, &out, &errOut); code != 1 {
		t.Errorf("import-tautulli: %d", code)
	}
}

func TestGrafanaBuildReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"nope"}`))
	}))
	defer srv.Close()
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"grafana-build", "-grafana-url", srv.URL, "-password", "x",
		"-movies-library", "Films"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "could not talk to grafana") {
		t.Errorf("grafana-build: %d %q", code, errOut.String())
	}
}
