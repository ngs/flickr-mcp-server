package flickr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type uploadSeen struct {
	fields   url.Values
	fileName string
	file     []byte
}

func uploadServer(t *testing.T, reply string) (*httptest.Server, *uploadSeen) {
	t.Helper()
	seen := &uploadSeen{fields: url.Values{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("part: %v", err)
				return
			}
			data, _ := io.ReadAll(part)
			if part.FormName() == "photo" {
				seen.fileName = part.FileName()
				seen.file = data
				continue
			}
			seen.fields.Add(part.FormName(), string(data))
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func tempFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fish.jpg")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUploadSignsTheFieldsButNotTheFile(t *testing.T) {
	srv, seen := uploadServer(t, `<?xml version="1.0" encoding="utf-8" ?><rsp stat="ok"><photoid>1234</photoid></rsp>`)
	c := NewClient(Credentials{APIKey: "key", APISecret: "secret"}, &Token{Token: "tok", TokenSecret: "ts"})
	c.UploadURL = srv.URL + "/services/upload/"
	c.signer = fixedSigner("n", 1700000000)

	res, err := c.Upload(context.Background(), tempFile(t, "JPEGDATA"), url.Values{"title": {"Sea bass"}, "is_public": {"0"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.PhotoID != "1234" || res.TicketID != "" {
		t.Fatalf("result = %+v", res)
	}
	if seen.fileName != "fish.jpg" || !bytes.Equal(seen.file, []byte("JPEGDATA")) {
		t.Fatalf("file = %s %q", seen.fileName, seen.file)
	}
	if seen.fields.Get("title") != "Sea bass" || seen.fields.Get("api_key") != "key" || seen.fields.Get("oauth_token") != "tok" {
		t.Fatalf("fields = %v", seen.fields)
	}
	want := signature("POST", c.UploadURL, seen.fields, "secret", "ts")
	if seen.fields.Get("oauth_signature") != want {
		t.Fatal("the signature must cover the form fields and not the file")
	}
}

func TestReplaceSendsThePhotoIDAndReadsATicket(t *testing.T) {
	srv, seen := uploadServer(t, `<rsp stat="ok"><ticketid>t-9</ticketid></rsp>`)
	c := NewClient(Credentials{APIKey: "key", APISecret: "secret"}, &Token{Token: "tok", TokenSecret: "ts"})
	c.ReplaceURL = srv.URL + "/services/replace/"
	res, err := c.Replace(context.Background(), tempFile(t, "x"), "55", url.Values{"async": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.TicketID != "t-9" || seen.fields.Get("photo_id") != "55" || seen.fields.Get("async") != "1" {
		t.Fatalf("res = %+v fields = %v", res, seen.fields)
	}
}

func TestUploadFailureBecomesAPIError(t *testing.T) {
	srv, _ := uploadServer(t, `<rsp stat="fail"><err code="5" msg="Filetype was not recognised" /></rsp>`)
	c := NewClient(Credentials{APIKey: "key", APISecret: "secret"}, &Token{Token: "tok", TokenSecret: "ts"})
	c.UploadURL = srv.URL + "/"
	_, err := c.Upload(context.Background(), tempFile(t, "x"), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 5 {
		t.Fatalf("err = %v", err)
	}
}

func TestUploadNeedsAToken(t *testing.T) {
	c := NewClient(Credentials{APIKey: "key", APISecret: "secret"}, nil)
	if _, err := c.Upload(context.Background(), "x", nil); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadPicksTheSizeByLabel(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services/rest/":
			_, _ = io.WriteString(w, `{"stat":"ok","sizes":{"size":[{"label":"Medium 800","source":"`+srvURL+`/m.jpg"},{"label":"Large","source":"`+srvURL+`/l.jpg"}]}}`)
		case "/l.jpg":
			_, _ = io.WriteString(w, "LARGE")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	c := NewClient(Credentials{APIKey: "key", APISecret: "secret"}, nil)
	c.RESTURL = srv.URL + "/services/rest/"

	var buf bytes.Buffer
	size, n, err := c.Download(context.Background(), "1", "large", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if size.Label != "Large" || n != 5 || buf.String() != "LARGE" {
		t.Fatalf("size=%+v n=%d body=%q", size, n, buf.String())
	}
	_, _, err = c.Download(context.Background(), "1", "Original", &buf)
	if err == nil || !strings.Contains(err.Error(), "Medium 800, Large") {
		t.Fatalf("a missing size must list the available ones: %v", err)
	}
}

func TestPickSizeAndFetch(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/l.jpg" {
			_, _ = io.WriteString(w, "LARGE")
			return
		}
		_, _ = io.WriteString(w, `{"stat":"ok","sizes":{"size":[{"label":"Large","source":"`+srvURL+`/l.jpg"}]}}`)
	}))
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	c := NewClient(Credentials{APIKey: "k", APISecret: "s"}, nil)
	c.RESTURL = srv.URL + "/rest"
	size, err := c.PickSize(context.Background(), "1", "LARGE")
	if err != nil || size.Source != srv.URL+"/l.jpg" {
		t.Fatalf("size = %+v, %v", size, err)
	}
	var buf bytes.Buffer
	n, err := c.Fetch(context.Background(), size.Source, &buf)
	if err != nil || n != 5 || buf.String() != "LARGE" {
		t.Fatalf("fetched %d %q, %v", n, buf.String(), err)
	}
}
