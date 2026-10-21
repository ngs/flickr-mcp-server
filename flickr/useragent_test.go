package flickr

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// live.staticflickr.com answers Go's default User-Agent with HTTP 502, so
// every request names this program instead.
func TestEveryRequestCarriesOurUserAgent(t *testing.T) {
	var mu sync.Mutex
	agents := map[string]string{}
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		agents[r.URL.Path] = r.Header.Get("User-Agent")
		mu.Unlock()
		switch r.URL.Path {
		case "/services/rest/":
			_, _ = io.WriteString(w, `{"stat":"ok","sizes":{"size":[{"label":"Large","source":"`+srvURL+`/l.jpg"}]}}`)
		case "/oauth/request_token":
			_, _ = io.WriteString(w, "oauth_token=a&oauth_token_secret=b")
		case "/upload/":
			_, _ = io.WriteString(w, `<rsp stat="ok"><photoid>1</photoid></rsp>`)
		default:
			_, _ = io.WriteString(w, "IMG")
		}
	}))
	t.Cleanup(srv.Close)
	srvURL = srv.URL

	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, &Token{Token: "t", TokenSecret: "ts"})
	c.RESTURL = srv.URL + "/services/rest/"
	c.RequestTokenURL = srv.URL + "/oauth/request_token"
	c.UploadURL = srv.URL + "/upload/"
	ctx := context.Background()
	if _, _, _, err := c.RequestToken(ctx, PermsRead); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Download(ctx, "1", "Large", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upload(ctx, tempFile(t, "x"), url.Values{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/services/rest/", "/oauth/request_token", "/l.jpg", "/upload/"} {
		if ua := agents[path]; !strings.HasPrefix(ua, "flickr-mcp-server/") {
			t.Errorf("%s: User-Agent = %q", path, ua)
		}
	}
}
