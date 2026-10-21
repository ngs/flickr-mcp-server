package flickr

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type recorded struct {
	method string
	query  url.Values
	form   url.Values
}

func testClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec recorded)) (*Client, *[]recorded) {
	t.Helper()
	var calls []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		rec := recorded{method: r.Method, query: r.URL.Query(), form: form}
		calls = append(calls, rec)
		handler(w, r, rec)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Credentials{APIKey: "key", APISecret: "secret"}, &Token{Token: "tok", TokenSecret: "toksecret", Perms: PermsRead})
	c.RESTURL = srv.URL + "/services/rest/"
	c.RequestTokenURL = srv.URL + "/oauth/request_token"
	c.AccessTokenURL = srv.URL + "/oauth/access_token"
	c.AuthorizeURL = "https://www.flickr.com/services/oauth/authorize"
	c.signer = fixedSigner("nonce", 1700000000)
	return c, &calls
}

func TestUnsignedCallSendsStandardParameters(t *testing.T) {
	c, calls := testClient(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) {
		_, _ = io.WriteString(w, `{"stat":"ok","photos":{"page":1}}`)
	})
	body, err := c.Call(context.Background(), Request{Method: "flickr.photos.search", Params: url.Values{"text": {"fish"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"stat":"ok","photos":{"page":1}}` {
		t.Fatalf("body = %s", body)
	}
	q := (*calls)[0].query
	for k, want := range map[string]string{"method": "flickr.photos.search", "format": "json", "nojsoncallback": "1", "api_key": "key", "text": "fish"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("oauth_signature") != "" {
		t.Error("an unsigned call must not carry an OAuth signature")
	}
}

func TestSignedPostCarriesAValidSignature(t *testing.T) {
	c, calls := testClient(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) {
		_, _ = io.WriteString(w, `{"stat":"ok"}`)
	})
	if _, err := c.Call(context.Background(), Request{Method: "flickr.photos.delete", Params: url.Values{"photo_id": {"1"}}, Sign: true, Post: true}); err != nil {
		t.Fatal(err)
	}
	rec := (*calls)[0]
	if rec.method != http.MethodPost {
		t.Fatalf("method = %s, want POST", rec.method)
	}
	if rec.form.Get("oauth_token") != "tok" || rec.form.Get("oauth_consumer_key") != "key" {
		t.Fatalf("missing oauth parameters: %v", rec.form)
	}
	want := signature("POST", c.RESTURL, rec.form, "secret", "toksecret")
	if got := rec.form.Get("oauth_signature"); got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

func TestSignedCallWithoutTokenFails(t *testing.T) {
	c, calls := testClient(t, func(http.ResponseWriter, *http.Request, recorded) {})
	c.Token = nil
	_, err := c.Call(context.Background(), Request{Method: "flickr.test.login", Sign: true})
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("err = %v, want ErrNotAuthorized", err)
	}
	if len(*calls) != 0 {
		t.Fatal("no request should be sent")
	}
}

func TestStatFailBecomesAPIError(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) {
		_, _ = io.WriteString(w, `{"stat":"fail","code":1,"message":"Photo not found"}`)
	})
	_, err := c.Call(context.Background(), Request{Method: "flickr.photos.getInfo"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 1 || apiErr.Message != "Photo not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPErrorIsReported(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request, _ recorded) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	if _, err := c.Call(context.Background(), Request{Method: "flickr.test.echo"}); err == nil {
		t.Fatal("want an error")
	}
}

func TestOAuthFlow(t *testing.T) {
	c, calls := testClient(t, func(w http.ResponseWriter, r *http.Request, _ recorded) {
		switch r.URL.Path {
		case "/oauth/request_token":
			_, _ = io.WriteString(w, "oauth_callback_confirmed=true&oauth_token=req&oauth_token_secret=reqsecret")
		case "/oauth/access_token":
			_, _ = io.WriteString(w, "fullname=Jane&oauth_token=acc&oauth_token_secret=accsecret&user_nsid=12%40N01&username=jane")
		}
	})
	token, secret, authURL, err := c.RequestToken(context.Background(), PermsWrite)
	if err != nil {
		t.Fatal(err)
	}
	if token != "req" || secret != "reqsecret" {
		t.Fatalf("request token = %s / %s", token, secret)
	}
	if authURL != "https://www.flickr.com/services/oauth/authorize?oauth_token=req&perms=write" {
		t.Fatalf("authorize URL = %s", authURL)
	}
	first := (*calls)[0].query
	if first.Get("oauth_callback") != "oob" {
		t.Fatalf("callback = %q, want oob", first.Get("oauth_callback"))
	}
	if first.Get("oauth_signature") != signature("GET", c.RequestTokenURL, first, "secret", "") {
		t.Fatal("request token call is not signed with the consumer secret alone")
	}

	got, err := c.AccessToken(context.Background(), token, secret, " 123-456 \n", PermsWrite)
	if err != nil {
		t.Fatal(err)
	}
	want := Token{Token: "acc", TokenSecret: "accsecret", UserNSID: "12@N01", Username: "jane", Perms: PermsWrite}
	if got != want {
		t.Fatalf("token = %+v, want %+v", got, want)
	}
	second := (*calls)[1].query
	if second.Get("oauth_verifier") != "123-456" || second.Get("oauth_token") != "req" {
		t.Fatalf("access token call = %v", second)
	}
	if second.Get("oauth_signature") != signature("GET", c.AccessTokenURL, second, "secret", "reqsecret") {
		t.Fatal("access token call is not signed with the request token secret")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

// A transport error must not carry the request URL's query: for signed GETs it
// holds the API key, the token, the signature and the verifier.
func TestTransportErrorsDoNotLeakTheQuery(t *testing.T) {
	c, _ := testClient(t, func(http.ResponseWriter, *http.Request, recorded) {})
	c.HTTP = &http.Client{Transport: failingTransport{}}

	_, err := c.Call(context.Background(), Request{Method: "flickr.test.login", Sign: true})
	if err == nil {
		t.Fatal("want an error")
	}
	_, err2 := c.AccessToken(context.Background(), "req", "reqsecret", "the-verifier", PermsRead)
	for _, e := range []error{err, err2} {
		msg := e.Error()
		for _, secret := range []string{"api_key=key", "oauth_token=", "oauth_signature=", "the-verifier", "?"} {
			if strings.Contains(msg, secret) {
				t.Fatalf("error leaks %q: %s", secret, msg)
			}
		}
		if !strings.Contains(msg, "connection refused") {
			t.Fatalf("error lost its cause: %s", msg)
		}
	}
}
