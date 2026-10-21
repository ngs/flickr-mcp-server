package flickr

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- OAuth 1.0a HMAC-SHA1 is what Flickr requires
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// signer adds OAuth 1.0a parameters and an HMAC-SHA1 signature (RFC 5849).
// nonce and now are replaceable so tests can check exact signatures.
type signer struct {
	nonce func() string
	now   func() time.Time
}

func defaultSigner() signer {
	return signer{nonce: randomNonce, now: time.Now}
}

func randomNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sign adds the oauth_* parameters and oauth_signature to params. token and
// tokenSecret are empty when requesting a request token.
func (s signer) sign(method, rawURL string, params url.Values, consumerKey, consumerSecret, token, tokenSecret string) {
	params.Set("oauth_consumer_key", consumerKey)
	params.Set("oauth_nonce", s.nonce())
	params.Set("oauth_signature_method", "HMAC-SHA1")
	params.Set("oauth_timestamp", strconv.FormatInt(s.now().Unix(), 10))
	params.Set("oauth_version", "1.0")
	if token != "" {
		params.Set("oauth_token", token)
	}
	params.Del("oauth_signature")
	params.Set("oauth_signature", signature(method, rawURL, params, consumerSecret, tokenSecret))
}

// signature computes the HMAC-SHA1 signature of the request.
func signature(method, rawURL string, params url.Values, consumerSecret, tokenSecret string) string {
	key := percentEncode(consumerSecret) + "&" + percentEncode(tokenSecret)
	mac := hmac.New(sha1.New, []byte(key))
	_, _ = mac.Write([]byte(baseString(method, rawURL, params)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// baseString is the signature base string of RFC 5849 section 3.4.1.
func baseString(method, rawURL string, params url.Values) string {
	type pair struct{ k, v string }
	var pairs []pair
	for k, vs := range params {
		if k == "oauth_signature" {
			continue
		}
		for _, v := range vs {
			pairs = append(pairs, pair{percentEncode(k), percentEncode(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.ToUpper(method) + "&" + percentEncode(normalizeURL(rawURL)) + "&" + percentEncode(strings.Join(parts, "&"))
}

// normalizeURL lowercases scheme and host, drops default ports, the query and
// the fragment (RFC 5849 section 3.4.1.2).
func normalizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if (scheme == "http" && strings.HasSuffix(host, ":80")) || (scheme == "https" && strings.HasSuffix(host, ":443")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

// percentEncode encodes everything except the RFC 3986 unreserved characters.
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
	}
	return b.String()
}
