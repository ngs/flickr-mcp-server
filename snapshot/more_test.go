package snapshot

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
	"time"
)

func TestHTTPFetcher(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "page")
	}))
	t.Cleanup(srv.Close)
	fetch := HTTPFetcher(srv.Client())
	if body, err := fetch(context.Background(), srv.URL+"/ok"); err != nil || body != "page" {
		t.Fatalf("got %q, %v", body, err)
	}
	if _, err := fetch(context.Background(), srv.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	if _, err := fetch(context.Background(), "http://127.0.0.1:1/"); err == nil {
		t.Fatal("a connection failure must be reported")
	}
	if _, err := fetch(context.Background(), "::bad"); err == nil {
		t.Fatal("a bad URL must be reported")
	}
}

func pagesFetcher(pages map[string]string) Fetcher {
	return func(_ context.Context, url string) (string, error) {
		p, ok := pages[url]
		if !ok {
			return "", errors.New("no page " + url)
		}
		return p, nil
	}
}

func TestFromDocsErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := FromDocs(ctx, pagesFetcher(nil), "https://d/", 0); err == nil {
		t.Error("a missing index must fail")
	}
	if _, err := FromDocs(ctx, pagesFetcher(map[string]string{"https://d/": "no links"}), "https://d/", 0); err == nil {
		t.Error("an index without methods must fail")
	}
	index := `<a href="/services/api/flickr.a.b.html">a</a>`
	if _, err := FromDocs(ctx, pagesFetcher(map[string]string{"https://d/": index}), "https://d/", 0); err == nil {
		t.Error("a missing method page must fail")
	}
	wrong := `<div class="InfoCase"><h1>flickr.other</h1><h3>Authentication</h3><p>This method does not require authentication.</p><h3>`
	if _, err := FromDocs(ctx, pagesFetcher(map[string]string{"https://d/": index, "https://d/flickr.a.b.html": wrong}), "https://d/", 0); err == nil {
		t.Error("a page describing another method must fail")
	}
	broken := `<div class="InfoCase"><h1>flickr.a.b</h1>`
	if _, err := FromDocs(ctx, pagesFetcher(map[string]string{"https://d/": index, "https://d/flickr.a.b.html": broken}), "https://d/", 0); err == nil {
		t.Error("a page without an Authentication section must fail")
	}
}

func TestFromDocsStopsWhenCancelled(t *testing.T) {
	page := func(name string) string {
		return `<div class="InfoCase"><h1>` + name + `</h1><h3>Authentication</h3><p>This method does not require authentication.</p><h3>`
	}
	pages := map[string]string{
		"https://d/":              `<a href="/services/api/flickr.a.html">a</a><a href="/services/api/flickr.b.html">b</a>`,
		"https://d/flickr.a.html": page("flickr.a"),
		"https://d/flickr.b.html": page("flickr.b"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	fetch := func(c context.Context, url string) (string, error) {
		if url == "https://d/flickr.a.html" {
			cancel()
		}
		return pagesFetcher(pages)(c, url)
	}
	if _, err := FromDocs(ctx, fetch, "https://d/", time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := FromDocs(context.Background(), pagesFetcher(pages), "https://d/", time.Millisecond); err != nil {
		t.Fatalf("with a delay: %v", err)
	}
	if MethodNamesFromIndex("") != nil {
		t.Fatal("no names from an empty page")
	}
}

func TestFromReflectionErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := FromReflection(ctx, fakeCaller{}, 0); err == nil {
		t.Error("a failing getMethods must fail")
	}
	if _, err := FromReflection(ctx, fakeCaller{"flickr.reflection.getMethods": `{"methods":5}`}, 0); err == nil {
		t.Error("a malformed getMethods must fail")
	}
	list := `{"methods":{"method":[{"_content":"flickr.a"},{"_content":"flickr.b"}]}}`
	if _, err := FromReflection(ctx, fakeCaller{"flickr.reflection.getMethods": list}, 0); err == nil {
		t.Error("a failing getMethodInfo must fail")
	}
	bad := fakeCaller{"flickr.reflection.getMethods": list, "flickr.reflection.getMethodInfo:flickr.a": `{"method":"x"}`}
	if _, err := FromReflection(ctx, bad, 0); err == nil {
		t.Error("a malformed getMethodInfo must fail")
	}
	info := func(n string) string {
		return `{"method":{"name":"` + n + `","requiredperms":""},"arguments":{"argument":[]}}`
	}
	ok := fakeCaller{"flickr.reflection.getMethods": list, "flickr.reflection.getMethodInfo:flickr.a": info("flickr.a"), "flickr.reflection.getMethodInfo:flickr.b": info("flickr.b")}
	ctx2, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := FromReflection(ctx2, ok, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	s, err := FromReflection(ctx, ok, time.Millisecond)
	if err != nil || len(s.Methods) != 2 {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestFlexInt(t *testing.T) {
	var f flexInt
	for in, want := range map[string]int{`3`: 3, `"2"`: 2, `""`: 0} {
		if err := json.Unmarshal([]byte(in), &f); err != nil || int(f) != want {
			t.Errorf("%s: %d, %v", in, f, err)
		}
	}
	for _, bad := range []string{`"x"`, `true`} {
		if err := json.Unmarshal([]byte(bad), &f); err == nil {
			t.Errorf("%s must fail", bad)
		}
	}
}

func TestReadAndWriteErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing file must fail")
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o600)
	if _, err := Read(bad); err == nil {
		t.Error("a broken file must fail")
	}
	if err := (&Snapshot{}).Write(filepath.Join(dir, "no", "such", "dir.json")); err == nil {
		t.Error("an unwritable path must fail")
	}
}

func TestParseMethodPageNeedsAName(t *testing.T) {
	if _, err := ParseMethodPage(`<h3>Authentication</h3><p>This method does not require authentication.</p><h3>`); err == nil {
		t.Fatal("a page without the method name must fail")
	}
}
