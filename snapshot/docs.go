package snapshot

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DocsBaseURL is where the API documentation lives.
const DocsBaseURL = "https://www.flickr.com/services/api/"

var (
	methodLinkRe  = regexp.MustCompile(`href="/services/api/(flickr\.[A-Za-z0-9.]+)\.html"`)
	titleRe       = regexp.MustCompile(`(?s)<div class="InfoCase">.*?<h1>([^<]+)</h1>`)
	descriptionRe = regexp.MustCompile(`(?s)<div class="MethodDescription">(.*?)</div>`)
	authRe        = regexp.MustCompile(`(?s)<h3>Authentication</h3>(.*?)<h3>`)
	permsRe       = regexp.MustCompile(`with '(read|write|delete)' permission`)
	argsRe        = regexp.MustCompile(`(?s)<h3>Arguments</h3>(.*?)(?:<h3>|$)`)
	argRe         = regexp.MustCompile(`(?s)<dt>\s*<b[^>]*>\s*<code>([^<]+)</code>\s*</b>\s*\((Required|Optional)\)\s*</dt>\s*<dd>(.*?)</dd>`)
	tagRe         = regexp.MustCompile(`<[^>]+>`)
	spaceBeforeRe = regexp.MustCompile(`\s+([.,;:!?)])`)
)

// Fetcher gets a page. Tests replace it.
type Fetcher func(ctx context.Context, url string) (string, error)

// HTTPFetcher fetches pages over HTTP.
func HTTPFetcher(client *http.Client) Fetcher {
	return func(ctx context.Context, url string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		return string(body), err
	}
}

// FromDocs builds a snapshot from the documentation pages: the method list
// from the index, then one page per method. delay spaces out the requests.
func FromDocs(ctx context.Context, fetch Fetcher, baseURL string, delay time.Duration) (*Snapshot, error) {
	index, err := fetch(ctx, baseURL)
	if err != nil {
		return nil, fmt.Errorf("fetching the method list: %w", err)
	}
	names := MethodNamesFromIndex(index)
	if len(names) == 0 {
		return nil, fmt.Errorf("no methods found in %s", baseURL)
	}
	s := &Snapshot{Source: "docs"}
	for i, name := range names {
		if i > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		page, err := fetch(ctx, baseURL+name+".html")
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", name, err)
		}
		m, err := ParseMethodPage(page)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if m.Name != name {
			return nil, fmt.Errorf("page for %s describes %s", name, m.Name)
		}
		s.Methods = append(s.Methods, m)
	}
	s.Normalize()
	return s, nil
}

// MethodNamesFromIndex lists the method names linked from the index page.
func MethodNamesFromIndex(page string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range methodLinkRe.FindAllStringSubmatch(page, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return names
}

// ParseMethodPage reads one method documentation page.
func ParseMethodPage(page string) (Method, error) {
	var m Method
	title := titleRe.FindStringSubmatch(page)
	if title == nil {
		return m, fmt.Errorf("no method name on the page")
	}
	m.Name = strings.TrimSpace(title[1])
	if d := descriptionRe.FindStringSubmatch(page); d != nil {
		m.Description = htmlText(d[1])
	}
	auth := authRe.FindStringSubmatch(page)
	if auth == nil {
		return m, fmt.Errorf("no Authentication section")
	}
	// Match on the text: the pages put markup inside these sentences.
	authText := htmlText(auth[1])
	if p := permsRe.FindStringSubmatch(authText); p != nil {
		m.RequiredPerms = map[string]int{"read": 1, "write": 2, "delete": 3}[p[1]]
	} else if strings.Contains(authText, "the request must be signed") {
		m.NeedsSigning = true
	} else if !strings.Contains(authText, "does not require authentication") {
		return m, fmt.Errorf("unrecognised Authentication section: %q", authText)
	}
	m.Post = strings.Contains(authText, "requires an HTTP POST request")
	if args := argsRe.FindStringSubmatch(page); args != nil {
		for _, a := range argRe.FindAllStringSubmatch(args[1], -1) {
			m.Arguments = append(m.Arguments, Argument{
				Name:        strings.TrimSpace(a[1]),
				Optional:    a[2] == "Optional",
				Description: htmlText(a[3]),
			})
		}
	}
	return m, nil
}

func htmlText(s string) string {
	// Tags become spaces so block elements do not glue words together; the
	// space that leaves before punctuation after an inline tag is removed.
	return spaceBeforeRe.ReplaceAllString(cleanText(html.UnescapeString(tagRe.ReplaceAllString(s, " "))), "$1")
}
