package flickr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDirFollowsXDGAndHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if d, _ := ConfigDir(); d != filepath.Join("/xdg", "flickr-mcp-server") {
		t.Fatalf("got %s", d)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	if d, _ := ConfigDir(); d != filepath.Join(home, ".config", "flickr-mcp-server") {
		t.Fatalf("got %s", d)
	}
}

func TestPendingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Pending{Token: "r", TokenSecret: "rs", Perms: PermsDelete}
	if err := SavePending(dir, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadPending(dir)
	if err != nil || out != in {
		t.Fatalf("got %+v, %v", out, err)
	}
	if err := RemovePending(dir); err != nil {
		t.Fatal(err)
	}
	if err := RemovePending(dir); err != nil {
		t.Fatal("removing a missing pending file is not an error")
	}
	if _, err := LoadPending(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestBrokenSecretFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"token.json", "pending"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadToken(dir); err == nil {
		t.Error("a broken token file must fail")
	}
	if _, err := LoadPending(dir); err == nil {
		t.Error("a broken pending file must fail")
	}
}

func TestWriteSecretFailsWhenTheDirectoryIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(file, Token{}); err == nil {
		t.Fatal("want an error")
	}
}

func TestPermsEdges(t *testing.T) {
	if got := Perms(9).String(); got != "Perms(9)" {
		t.Fatalf("got %s", got)
	}
	var p Perms
	if err := json.Unmarshal([]byte(`"admin"`), &p); err == nil {
		t.Error("an unknown name must fail")
	}
	if err := json.Unmarshal([]byte(`3`), &p); err == nil {
		t.Error("a number must fail")
	}
}

func TestAPIErrorMessage(t *testing.T) {
	if got := (&APIError{Code: 98, Message: "Invalid auth token"}).Error(); got != "flickr error 98: Invalid auth token" {
		t.Fatalf("got %q", got)
	}
}

func TestCallNeedsAKeyAndValidJSON(t *testing.T) {
	c := NewClient(Credentials{}, nil)
	if _, err := c.Call(context.Background(), Request{Method: "flickr.test.echo"}); err == nil || !strings.Contains(err.Error(), "setup") {
		t.Fatalf("err = %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>") }))
	t.Cleanup(srv.Close)
	c = NewClient(Credentials{APIKey: "k"}, nil)
	c.RESTURL = srv.URL
	if _, err := c.Call(context.Background(), Request{Method: "flickr.test.echo"}); err == nil {
		t.Fatal("a non-JSON body must fail")
	}
}

func TestTokenEndpointsReportBadReplies(t *testing.T) {
	reply := ""
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, nil)
	c.RequestTokenURL = srv.URL + "/request"
	c.AccessTokenURL = srv.URL + "/access"
	ctx := context.Background()

	for _, r := range []string{"oauth_problem=signature_invalid", "%zz"} {
		reply = r
		if _, _, _, err := c.RequestToken(ctx, PermsRead); err == nil {
			t.Errorf("request token reply %q must fail", r)
		}
		if _, err := c.AccessToken(ctx, "a", "b", "c", PermsRead); err == nil {
			t.Errorf("access token reply %q must fail", r)
		}
	}
	status = http.StatusUnauthorized
	reply = "oauth_problem=token_rejected"
	if _, _, _, err := c.RequestToken(ctx, PermsRead); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.AccessToken(ctx, "a", "b", "c", PermsRead); err == nil {
		t.Fatal("an HTTP error must fail")
	}
}

func TestSizesAndDownloadErrors(t *testing.T) {
	body := `{"stat":"ok","sizes":"nope"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/img" {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, nil)
	c.RESTURL = srv.URL + "/rest"
	if _, err := c.Sizes(context.Background(), "1"); err == nil {
		t.Fatal("a malformed getSizes reply must fail")
	}
	body = `{"stat":"fail","code":1,"message":"Photo not found"}`
	if _, _, err := c.Download(context.Background(), "1", "Large", io.Discard); err == nil {
		t.Fatal("a getSizes failure must fail the download")
	}
	body = `{"stat":"ok","sizes":{"size":[{"label":"Large","source":"` + srv.URL + `/img"}]}}`
	if _, _, err := c.Download(context.Background(), "1", "Large", io.Discard); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestUploadErrors(t *testing.T) {
	status, reply := http.StatusOK, "not xml"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, &Token{Token: "t", TokenSecret: "ts"})
	c.UploadURL = srv.URL
	ctx := context.Background()
	if _, err := c.Upload(ctx, filepath.Join(t.TempDir(), "missing.jpg"), nil); err == nil {
		t.Error("a missing file must fail")
	}
	file := tempFile(t, "x")
	if _, err := c.Upload(ctx, file, nil); err == nil {
		t.Error("a non-XML reply must fail")
	}
	status = http.StatusInternalServerError
	if _, err := c.Upload(ctx, file, nil); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v", err)
	}
	c.UploadURL = "http://127.0.0.1:1/upload"
	if _, err := c.Upload(ctx, file, nil); err == nil || strings.Contains(err.Error(), "oauth_token") {
		t.Errorf("a transport error must fail without leaking parameters: %v", err)
	}
}

func TestDefaultUploadEndpoints(t *testing.T) {
	c := NewClient(Credentials{}, nil)
	if c.uploadURL() != DefaultUploadURL || c.replaceURL() != DefaultReplaceURL {
		t.Fatal("default endpoints")
	}
}
