package flickr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default endpoints.
const (
	DefaultRESTURL         = "https://www.flickr.com/services/rest/"
	DefaultRequestTokenURL = "https://www.flickr.com/services/oauth/request_token"
	DefaultAuthorizeURL    = "https://www.flickr.com/services/oauth/authorize"
	DefaultAccessTokenURL  = "https://www.flickr.com/services/oauth/access_token"
)

// Client calls the Flickr API.
type Client struct {
	HTTP  *http.Client
	Creds Credentials
	// Token is nil until the user has authorized; signed calls then fail.
	Token *Token

	RESTURL         string
	RequestTokenURL string
	AuthorizeURL    string
	AccessTokenURL  string

	signer signer
}

// NewClient returns a client for the real endpoints.
func NewClient(creds Credentials, token *Token) *Client {
	return &Client{
		HTTP:            &http.Client{Timeout: 60 * time.Second},
		Creds:           creds,
		Token:           token,
		RESTURL:         DefaultRESTURL,
		RequestTokenURL: DefaultRequestTokenURL,
		AuthorizeURL:    DefaultAuthorizeURL,
		AccessTokenURL:  DefaultAccessTokenURL,
		signer:          defaultSigner(),
	}
}

// APIError is a Flickr "stat":"fail" response.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return fmt.Sprintf("flickr error %d: %s", e.Code, e.Message) }

// ErrNotAuthorized is returned for a signed call without a token.
var ErrNotAuthorized = errors.New("not authorized yet; run `flickr-mcp-server auth`")

// Request describes one REST call.
type Request struct {
	Method string     // e.g. "flickr.photos.search"
	Params url.Values // method arguments, without api_key
	Sign   bool       // OAuth-sign with the user's token
	Post   bool       // send as an HTTP POST
}

// Call performs a REST call and returns the JSON body of a successful
// response. A "stat":"fail" response becomes an *APIError.
func (c *Client) Call(ctx context.Context, r Request) (json.RawMessage, error) {
	if c.Creds.APIKey == "" {
		return nil, errors.New("no API key; run `flickr-mcp-server setup` or set " + EnvAPIKey)
	}
	params := url.Values{}
	for k, vs := range r.Params {
		params[k] = append([]string(nil), vs...)
	}
	params.Set("method", r.Method)
	params.Set("format", "json")
	params.Set("nojsoncallback", "1")
	params.Set("api_key", c.Creds.APIKey)

	httpMethod := http.MethodGet
	if r.Post {
		httpMethod = http.MethodPost
	}
	if r.Sign {
		if c.Token == nil || c.Token.Token == "" {
			return nil, ErrNotAuthorized
		}
		c.signer.sign(httpMethod, c.RESTURL, params, c.Creds.APIKey, c.Creds.APISecret, c.Token.Token, c.Token.TokenSecret)
	}

	body, err := c.do(ctx, httpMethod, c.RESTURL, params)
	if err != nil {
		return nil, err
	}
	var stat struct {
		Stat string `json:"stat"`
		APIError
	}
	if err := json.Unmarshal(body, &stat); err != nil {
		return nil, fmt.Errorf("decoding the response of %s: %w", r.Method, err)
	}
	if stat.Stat != "ok" {
		e := stat.APIError
		return nil, &e
	}
	return body, nil
}

// do sends params as a query (GET) or form body (POST) and returns the body.
func (c *Client) do(ctx context.Context, method, rawURL string, params url.Values) ([]byte, error) {
	var req *http.Request
	var err error
	if method == http.MethodPost {
		req, err = http.NewRequestWithContext(ctx, method, rawURL, strings.NewReader(params.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, method, rawURL+"?"+params.Encode(), nil)
	}
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// *url.Error quotes the whole URL; for signed GETs its query holds the
		// API key, the token, the signature and the verifier.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, fmt.Errorf("%s %s: %w", method, normalizeURL(rawURL), uerr.Err)
		}
		return nil, fmt.Errorf("%s %s: %w", method, normalizeURL(rawURL), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, normalizeURL(rawURL), resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// RequestToken starts the OAuth flow with an out-of-band callback. It returns
// the request token, its secret and the URL the user opens to authorize.
func (c *Client) RequestToken(ctx context.Context, perms Perms) (token, secret, authorizeURL string, err error) {
	params := url.Values{"oauth_callback": {"oob"}}
	c.signer.sign(http.MethodGet, c.RequestTokenURL, params, c.Creds.APIKey, c.Creds.APISecret, "", "")
	body, err := c.do(ctx, http.MethodGet, c.RequestTokenURL, params)
	if err != nil {
		return "", "", "", fmt.Errorf("requesting a request token: %w", err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return "", "", "", fmt.Errorf("reading the request token: %w", err)
	}
	token, secret = values.Get("oauth_token"), values.Get("oauth_token_secret")
	if token == "" || secret == "" {
		return "", "", "", fmt.Errorf("no request token in the response")
	}
	authorize := url.Values{"oauth_token": {token}, "perms": {perms.String()}}
	return token, secret, c.AuthorizeURL + "?" + authorize.Encode(), nil
}

// AccessToken exchanges the authorized request token and the verifier the
// user copied from the browser for an access token.
func (c *Client) AccessToken(ctx context.Context, requestToken, requestSecret, verifier string, perms Perms) (Token, error) {
	params := url.Values{"oauth_verifier": {strings.TrimSpace(verifier)}}
	c.signer.sign(http.MethodGet, c.AccessTokenURL, params, c.Creds.APIKey, c.Creds.APISecret, requestToken, requestSecret)
	body, err := c.do(ctx, http.MethodGet, c.AccessTokenURL, params)
	if err != nil {
		return Token{}, fmt.Errorf("exchanging the verifier: %w", err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return Token{}, fmt.Errorf("reading the access token: %w", err)
	}
	t := Token{
		Token:       values.Get("oauth_token"),
		TokenSecret: values.Get("oauth_token_secret"),
		UserNSID:    values.Get("user_nsid"),
		Username:    values.Get("username"),
		Perms:       perms,
	}
	if t.Token == "" || t.TokenSecret == "" {
		return Token{}, fmt.Errorf("no access token in the response")
	}
	return t, nil
}
