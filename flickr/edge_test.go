package flickr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// shortBody promises more bytes than it sends, so reading the body fails.
func shortBody(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"stat\":")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBrokenResponseBodies(t *testing.T) {
	srv := shortBody(t)
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, &Token{Token: "t", TokenSecret: "ts"})
	c.RESTURL = srv.URL
	c.UploadURL = srv.URL
	if _, err := c.Call(context.Background(), Request{Method: "flickr.test.echo"}); err == nil {
		t.Error("a cut REST body must fail")
	}
	if _, err := c.Upload(context.Background(), tempFile(t, "x"), nil); err == nil {
		t.Error("a cut upload reply must fail")
	}
}

func TestBadURLs(t *testing.T) {
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, &Token{Token: "t", TokenSecret: "ts"})
	c.RESTURL = "http://bad host/"
	for _, post := range []bool{false, true} {
		if _, err := c.Call(context.Background(), Request{Method: "flickr.test.echo", Post: post}); err == nil {
			t.Errorf("post=%v: a malformed endpoint must fail", post)
		}
	}
	c.UploadURL = "http://bad host/"
	if _, err := c.Upload(context.Background(), tempFile(t, "x"), nil); err == nil {
		t.Error("a malformed upload endpoint must fail")
	}
	if got := normalizeURL("http://bad host/"); got != "http://bad host/" {
		t.Errorf("an unparsable URL is used as it is, got %q", got)
	}
}

func TestDownloadSourceFailures(t *testing.T) {
	source := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"stat":"ok","sizes":{"size":[{"label":"Large","source":"`+source+`"}]}}`)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, nil)
	c.RESTURL = srv.URL
	for _, s := range []string{"http://bad host/x.jpg", "http://127.0.0.1:1/x.jpg"} {
		source = s
		if _, _, err := c.Download(context.Background(), "1", "Large", io.Discard); err == nil {
			t.Errorf("source %q must fail", s)
		}
	}
}

func TestUploadingADirectoryFails(t *testing.T) {
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, &Token{Token: "t", TokenSecret: "ts"})
	if _, err := c.Upload(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("a directory cannot be read as a photo")
	}
}

func TestConfigDirWithoutHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := ConfigDir(); err == nil {
		t.Fatal("no home directory must fail")
	}
}

func TestSecretFromTheEnvironment(t *testing.T) {
	t.Setenv(EnvAPISecret, "env-secret")
	c, err := LoadCredentials(t.TempDir())
	if err != nil || c.APISecret != "env-secret" {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestUnreadableSecretFiles(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be: private, but not readable as a file.
	for _, name := range []string{"token.json"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadToken(dir); err == nil {
		t.Error("a directory is not a token file")
	}
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		return
	}
	creds := filepath.Join(dir, "credentials")
	if err := os.WriteFile(creds, []byte("FLICKR_API_KEY=k\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(dir); err == nil {
		t.Error("an unreadable credentials file must fail")
	}
}

func TestWriteSecretFailures(t *testing.T) {
	dir := t.TempDir()
	// The temporary file cannot be created...
	if err := os.MkdirAll(filepath.Join(dir, "token.json.tmp", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(dir, Token{}); err == nil {
		t.Error("a blocked temporary file must fail")
	}
	// ...or cannot replace the file.
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "token.json", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(dir, Token{}); err == nil {
		t.Error("a blocked target must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json.tmp")); !os.IsNotExist(err) {
		t.Error("a failed save must not leave its temporary file")
	}
}
