package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.ngs.io/flickr-mcp-server/flickr"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseMethodPageWithoutAuthentication(t *testing.T) {
	m, err := ParseMethodPage(fixture(t, "flickr.photos.search.html"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "flickr.photos.search" || m.RequiredPerms != 0 || m.Post {
		t.Fatalf("got %s perms=%d post=%v", m.Name, m.RequiredPerms, m.Post)
	}
	if !strings.HasPrefix(m.Description, "Return a list of photos matching some criteria.") {
		t.Fatalf("description = %q", m.Description)
	}
	byName := map[string]Argument{}
	for _, a := range m.Arguments {
		byName[a.Name] = a
	}
	if a, ok := byName["user_id"]; !ok || !a.Optional || strings.Contains(a.Description, "<") {
		t.Fatalf("user_id = %+v", a)
	}
	if a := byName["api_key"]; a.Optional {
		t.Fatal("api_key is required on the page")
	}
}

func TestParseMethodPageWithDeletePermissionAndPost(t *testing.T) {
	m, err := ParseMethodPage(fixture(t, "flickr.photos.delete.html"))
	if err != nil {
		t.Fatal(err)
	}
	if m.RequiredPerms != 3 || !m.Post {
		t.Fatalf("perms=%d post=%v", m.RequiredPerms, m.Post)
	}
	if len(m.Arguments) != 2 || m.Arguments[1].Name != "photo_id" || m.Arguments[1].Optional {
		t.Fatalf("arguments = %+v", m.Arguments)
	}
}

func TestParseMethodPageSignedWithoutUser(t *testing.T) {
	page := `<div class="InfoCase"><h1>flickr.auth.oauth.checkToken</h1><h3>Authentication</h3><p>This method does not require user authentication, but the request <b>must</b> be <a href="/services/api/auth.oauth.html#signing">signed</a>.</p><h3>Arguments</h3><dl></dl>`
	m, err := ParseMethodPage(page)
	if err != nil {
		t.Fatal(err)
	}
	if !m.NeedsSigning || m.NeedsLogin || m.RequiredPerms != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestParseMethodPageRejectsUnknownAuthentication(t *testing.T) {
	page := `<div class="InfoCase"><h1>flickr.x</h1><h3>Authentication</h3><p>Something new.</p><h3>Arguments</h3>`
	if _, err := ParseMethodPage(page); err == nil {
		t.Fatal("an unrecognised Authentication section must fail loudly")
	}
}

func TestFromDocs(t *testing.T) {
	pages := map[string]string{
		"https://example.test/api/":                          fixture(t, "index.html"),
		"https://example.test/api/flickr.photos.search.html": fixture(t, "flickr.photos.search.html"),
		"https://example.test/api/flickr.photos.delete.html": fixture(t, "flickr.photos.delete.html"),
	}
	fetch := func(_ context.Context, url string) (string, error) {
		p, ok := pages[url]
		if !ok {
			return "", fmt.Errorf("unexpected fetch %s", url)
		}
		return p, nil
	}
	s, err := FromDocs(context.Background(), fetch, "https://example.test/api/", 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "docs" || len(s.Methods) != 2 {
		t.Fatalf("got %s with %d methods", s.Source, len(s.Methods))
	}
	if s.Methods[0].Name != "flickr.photos.delete" {
		t.Fatal("methods must be sorted by name")
	}
	del := s.Methods[0]
	if !del.NeedsLogin || !del.NeedsSigning {
		t.Fatal("a method with a required permission needs login and signing")
	}
	for _, m := range s.Methods {
		for _, a := range m.Arguments {
			if a.Name == "api_key" {
				t.Fatalf("%s still lists api_key", m.Name)
			}
		}
	}
}

type fakeCaller map[string]string

func (f fakeCaller) Call(_ context.Context, r flickr.Request) (json.RawMessage, error) {
	key := r.Method
	if n := r.Params.Get("method_name"); n != "" {
		key += ":" + n
	}
	body, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("unexpected call %s", key)
	}
	return json.RawMessage(body), nil
}

func TestFromReflection(t *testing.T) {
	caller := fakeCaller{
		"flickr.reflection.getMethods": `{"methods":{"method":[{"_content":"flickr.photos.setTags"},{"_content":"flickr.test.echo"}]},"stat":"ok"}`,
		"flickr.reflection.getMethodInfo:flickr.photos.setTags": `{"method":{"name":"flickr.photos.setTags","needslogin":1,"needssigning":1,"requiredperms":"2","description":{"_content":"Set the tags for a <b>photo</b>."}},
			"arguments":{"argument":[{"name":"api_key","optional":0,"_content":"key"},{"name":"photo_id","optional":"0","_content":"The id"},{"name":"tags","optional":0,"_content":"All tags"}]},"stat":"ok"}`,
		"flickr.reflection.getMethodInfo:flickr.test.echo": `{"method":{"name":"flickr.test.echo","needslogin":0,"needssigning":0,"requiredperms":0,"description":{"_content":"A testing method which echo's all parameters back in the response."}},"arguments":{"argument":[{"name":"api_key","optional":0,"_content":"key"}]},"stat":"ok"}`,
	}
	s, err := FromReflection(context.Background(), caller, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "reflection" || len(s.Methods) != 2 {
		t.Fatalf("got %+v", s)
	}
	set := s.Methods[0]
	if set.RequiredPerms != 2 || !set.Post || set.Description != "Set the tags for a photo." {
		t.Fatalf("setTags = %+v", set)
	}
	if len(set.Arguments) != 2 || set.Arguments[0].Name != "photo_id" || set.Arguments[0].Optional {
		t.Fatalf("arguments = %+v", set.Arguments)
	}
	if echo := s.Methods[1]; echo.RequiredPerms != 0 || echo.Post || len(echo.Arguments) != 0 {
		t.Fatalf("echo = %+v", echo)
	}
}

func TestWriteAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "methods.json")
	in := &Snapshot{Source: "docs", Methods: []Method{{Name: "flickr.test.echo", Arguments: []Argument{}}}}
	if err := in.Write(path); err != nil {
		t.Fatal(err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != "docs" || len(out.Methods) != 1 || out.Methods[0].Name != "flickr.test.echo" {
		t.Fatalf("got %+v", out)
	}
}

// An argument description can hold its own <dl> of values; the arguments
// after it must not be lost.
func TestParseMethodPageWithNestedDefinitionList(t *testing.T) {
	m, err := ParseMethodPage(fixture(t, "flickr.contacts.getList.html"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range m.Arguments {
		names = append(names, a.Name)
	}
	if got := strings.Join(names, ","); got != "api_key,filter,page,per_page,sort" {
		t.Fatalf("arguments = %s", got)
	}
}
