package flickr

import (
	"net/url"
	"testing"
	"time"
)

func fixedSigner(nonce string, unix int64) signer {
	return signer{nonce: func() string { return nonce }, now: func() time.Time { return time.Unix(unix, 0) }}
}

// The example from the OAuth Core 1.0 specification (Appendix A.5), which
// RFC 5849 keeps compatible.
func TestSignatureMatchesTheOAuthSpecExample(t *testing.T) {
	params := url.Values{"file": {"vacation.jpg"}, "size": {"original"}}
	fixedSigner("kllo9940pd9333jh", 1191242096).sign("GET", "http://photos.example.net/photos", params,
		"dpf43f3p2l4k3l03", "kd94hf93k423kf44", "nnch734d00sl2jdk", "pfkkdhi9sl3r4s00")

	wantBase := "GET&http%3A%2F%2Fphotos.example.net%2Fphotos&file%3Dvacation.jpg%26oauth_consumer_key%3Ddpf43f3p2l4k3l03%26oauth_nonce%3Dkllo9940pd9333jh%26oauth_signature_method%3DHMAC-SHA1%26oauth_timestamp%3D1191242096%26oauth_token%3Dnnch734d00sl2jdk%26oauth_version%3D1.0%26size%3Doriginal"
	if got := baseString("GET", "http://photos.example.net/photos", params); got != wantBase {
		t.Fatalf("base string\n got %s\nwant %s", got, wantBase)
	}
	if got, want := params.Get("oauth_signature"), "tR3+Ty81lMeYAr/Fid0kMTYa/WM="; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

func TestPercentEncodeFollowsRFC3986(t *testing.T) {
	cases := map[string]string{
		"abcABC123-._~": "abcABC123-._~",
		"a b":           "a%20b",
		"a+b=c&d":       "a%2Bb%3Dc%26d",
		"日":             "%E6%97%A5",
		"*!'()":         "%2A%21%27%28%29",
	}
	for in, want := range cases {
		if got := percentEncode(in); got != want {
			t.Errorf("percentEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeURLDropsDefaultPortQueryAndFragment(t *testing.T) {
	cases := map[string]string{
		"HTTPS://WWW.Flickr.com:443/services/rest/?a=1#x": "https://www.flickr.com/services/rest/",
		"http://example.com:80":                           "http://example.com/",
		"http://example.com:8080/p":                       "http://example.com:8080/p",
	}
	for in, want := range cases {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignatureCoversRepeatedParameters(t *testing.T) {
	a := url.Values{"tag": {"b", "a"}}
	b := url.Values{"tag": {"a", "b"}}
	if baseString("GET", "https://x/", a) != baseString("GET", "https://x/", b) {
		t.Fatal("parameter value order must not change the base string")
	}
}
