package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/flickr"
)

type fakeCaller struct {
	mu    sync.Mutex
	calls []flickr.Request
	reply json.RawMessage
	err   error
}

func (f *fakeCaller) Call(_ context.Context, r flickr.Request) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
	if f.err != nil {
		return nil, f.err
	}
	return f.reply, nil
}

func (f *fakeCaller) last(t *testing.T) flickr.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no call was made")
	}
	return f.calls[len(f.calls)-1]
}

func session(t *testing.T, c Caller, opts Options) (*mcp.ClientSession, int) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	n := Register(srv, c, opts)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, n
}

func listNames(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	out := map[string]*mcp.Tool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out[tool.Name] = tool
	}
	return out
}

func TestTheSnapshotIsComplete(t *testing.T) {
	if len(methods) < 200 {
		t.Fatalf("only %d methods in the generated table", len(methods))
	}
	seen := map[string]bool{}
	for _, m := range methods {
		if seen[m.ToolName()] {
			t.Fatalf("duplicate tool name %s", m.ToolName())
		}
		seen[m.ToolName()] = true
		if m.RequiredPerms < 0 || m.RequiredPerms > 3 {
			t.Fatalf("%s: bad permission %d", m.Name, m.RequiredPerms)
		}
	}
}

// Every generated tool: required arguments are enforced, values reach the
// request, and the call is signed and POSTed as the method needs.
func TestEveryGeneratedToolBuildsItsRequest(t *testing.T) {
	caller := &fakeCaller{reply: json.RawMessage(`{"stat":"ok"}`)}
	cs, n := session(t, caller, Options{MaxPerms: flickr.PermsDelete, Authorized: true})
	tools := listNames(t, cs)
	if len(tools) != n {
		t.Fatalf("listed %d tools, registered %d", len(tools), n)
	}
	for _, m := range Selected(Options{MaxPerms: flickr.PermsDelete, Authorized: true}) {
		t.Run(m.Name, func(t *testing.T) {
			if _, ok := tools[m.ToolName()]; !ok {
				t.Fatalf("tool %s is not listed", m.ToolName())
			}
			args := map[string]any{}
			for _, a := range m.Arguments {
				if !a.Optional {
					args[a.Name] = "v-" + a.Name
				}
			}
			before := len(caller.calls)
			if hasRequired(m) {
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: m.ToolName(), Arguments: map[string]any{}})
				if err != nil {
					t.Fatal(err)
				}
				if !res.IsError || len(caller.calls) != before {
					t.Fatal("a call without its required arguments must fail before reaching Flickr")
				}
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: m.ToolName(), Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("unexpected error result: %v", res.Content)
			}
			r := caller.last(t)
			if r.Method != m.Name || r.Post != m.Post || !r.Sign {
				t.Fatalf("request = %+v", r)
			}
			for k, v := range args {
				if r.Params.Get(k) != v {
					t.Fatalf("%s = %q, want %q", k, r.Params.Get(k), v)
				}
			}
		})
	}
}

func hasRequired(m Method) bool {
	for _, a := range m.Arguments {
		if !a.Optional {
			return true
		}
	}
	return false
}

func TestPermissionCeilingHidesStrongerTools(t *testing.T) {
	for _, max := range []flickr.Perms{flickr.PermsRead, flickr.PermsWrite, flickr.PermsDelete} {
		cs, _ := session(t, &fakeCaller{}, Options{MaxPerms: max, Authorized: true})
		tools := listNames(t, cs)
		for _, m := range methods {
			if _, listed := tools[m.ToolName()]; listed && m.Perms() > max {
				t.Fatalf("max %s lists %s, which needs %s", max, m.Name, m.Perms())
			}
		}
		if _, ok := tools["flickr_photos_delete"]; ok != (max == flickr.PermsDelete) {
			t.Fatalf("max %s: flickr_photos_delete listed = %v", max, ok)
		}
	}
}

func TestWithoutTokenOnlyPublicMethodsAreListedAndCallsAreUnsigned(t *testing.T) {
	caller := &fakeCaller{reply: json.RawMessage(`{"stat":"ok"}`)}
	cs, _ := session(t, caller, Options{MaxPerms: flickr.PermsDelete})
	tools := listNames(t, cs)
	if _, ok := tools["flickr_test_login"]; ok {
		t.Fatal("flickr.test.login needs a user and must not be listed without a token")
	}
	if _, ok := tools["flickr_photos_search"]; !ok {
		t.Fatal("flickr.photos.search must be listed without a token")
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "flickr_photos_search", Arguments: map[string]any{"text": "fish"}}); err != nil {
		t.Fatal(err)
	}
	if caller.last(t).Sign {
		t.Fatal("calls must not be signed without a token")
	}
}

func TestNamespacesFilter(t *testing.T) {
	for _, m := range Selected(Options{MaxPerms: flickr.PermsDelete, Authorized: true, Namespaces: []string{"photosets", " tags"}}) {
		if ns := m.Namespace(); ns != "photosets" && ns != "tags" {
			t.Fatalf("%s is outside the requested namespaces", m.Name)
		}
	}
}

func TestAuthMethodsAreNotTools(t *testing.T) {
	for _, m := range Selected(Options{MaxPerms: flickr.PermsDelete, Authorized: true}) {
		if strings.HasPrefix(m.Name, "flickr.auth.") {
			t.Fatalf("%s should be excluded", m.Name)
		}
	}
}

func TestArgumentConversionAndValidation(t *testing.T) {
	caller := &fakeCaller{reply: json.RawMessage(`{"stat":"ok"}`)}
	cs, _ := session(t, caller, Options{MaxPerms: flickr.PermsRead, Authorized: true})
	ctx := context.Background()

	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "flickr_photos_search", Arguments: map[string]any{"per_page": 20, "has_geo": true, "text": "a b"}}); err != nil {
		t.Fatal(err)
	}
	p := caller.last(t).Params
	if p.Get("per_page") != "20" || p.Get("has_geo") != "1" || p.Get("text") != "a b" {
		t.Fatalf("params = %v", p)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "flickr_photos_search", Arguments: map[string]any{"nonsense": "x"}})
	if err != nil {
		// The SDK may reject it against the schema (additionalProperties: false).
		return
	}
	if !res.IsError {
		t.Fatal("an unknown argument must be rejected")
	}
}

func TestFlickrErrorsBecomeToolErrors(t *testing.T) {
	caller := &fakeCaller{err: &flickr.APIError{Code: 1, Message: "Photo not found"}}
	cs, _ := session(t, caller, Options{MaxPerms: flickr.PermsRead, Authorized: true})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "flickr_photos_getInfo", Arguments: map[string]any{"photo_id": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "Photo not found") {
		t.Fatalf("result = %+v", res)
	}
}

func TestReadOnlyAndDestructiveHints(t *testing.T) {
	cs, _ := session(t, &fakeCaller{}, Options{MaxPerms: flickr.PermsDelete, Authorized: true})
	tools := listNames(t, cs)
	if !tools["flickr_photos_search"].Annotations.ReadOnlyHint {
		t.Fatal("search is read-only")
	}
	del := tools["flickr_photos_delete"].Annotations
	if del.ReadOnlyHint || del.DestructiveHint == nil || !*del.DestructiveHint {
		t.Fatal("delete is destructive")
	}
}

func TestPagedMethodsGetASmallDefaultPerPage(t *testing.T) {
	caller := &fakeCaller{reply: json.RawMessage(`{"stat":"ok"}`)}
	cs, _ := session(t, caller, Options{MaxPerms: flickr.PermsRead, Authorized: true})
	ctx := context.Background()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "flickr_photos_search", Arguments: map[string]any{"text": "x"}}); err != nil {
		t.Fatal(err)
	}
	if got := caller.last(t).Params.Get("per_page"); got != DefaultPerPage {
		t.Fatalf("per_page = %q, want %s", got, DefaultPerPage)
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "flickr_photos_search", Arguments: map[string]any{"text": "x", "per_page": 500}}); err != nil {
		t.Fatal(err)
	}
	if got := caller.last(t).Params.Get("per_page"); got != "500" {
		t.Fatalf("an explicit per_page must win, got %q", got)
	}
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "flickr_photos_getInfo", Arguments: map[string]any{"photo_id": "1"}}); err != nil {
		t.Fatal(err)
	}
	if caller.last(t).Params.Has("per_page") {
		t.Fatal("methods without per_page must not get one")
	}
	tools := listNames(t, cs)
	if !strings.Contains(tools["flickr_photos_search"].Description, "per_page defaults to 50") {
		t.Fatal("paged tools must explain paging")
	}
}

func TestLongResponsesAreCut(t *testing.T) {
	big := `{"stat":"ok","x":"` + strings.Repeat("あ", 400) + `"}`
	caller := &fakeCaller{reply: json.RawMessage(big)}
	cs, _ := session(t, caller, Options{MaxPerms: flickr.PermsRead, Authorized: true, MaxResponseBytes: 100})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "flickr_photos_getExif", Arguments: map[string]any{"photo_id": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	head, note, ok := strings.Cut(text, "\n\n[truncated: ")
	if !ok || len(head) > 100 || !utf8.ValidString(head) {
		t.Fatalf("not cut on a character boundary within the limit: %q", text)
	}
	if !strings.Contains(note, "of "+strconv.Itoa(len(big))+" bytes") {
		t.Fatalf("note = %q", note)
	}
}

func TestShortResponsesAreUntouched(t *testing.T) {
	if got := truncate(`{"stat":"ok"}`, 100); got != `{"stat":"ok"}` {
		t.Fatalf("got %q", got)
	}
}

func TestScalarAndAccessors(t *testing.T) {
	if s, err := scalar(nil); err != nil || s != "" {
		t.Fatalf("nil: %q %v", s, err)
	}
	if _, err := scalar(map[string]any{}); err == nil {
		t.Fatal("an object is not a scalar")
	}
	if _, err := buildParams(Method{Name: "x"}, json.RawMessage(`[1]`)); err == nil {
		t.Fatal("arguments must be an object")
	}
	if _, err := buildParams(Method{Name: "x", Arguments: []Argument{{Name: "a", Optional: true}}}, json.RawMessage(`{"a":{"b":1}}`)); err == nil {
		t.Fatal("a nested value must be rejected")
	}
	if p, err := buildParams(Method{Name: "x"}, json.RawMessage(`null`)); err != nil || len(p) != 0 {
		t.Fatalf("null arguments: %v %v", p, err)
	}
	if len(Methods()) != len(methods) || SnapshotSource() == "" {
		t.Fatal("accessors")
	}
	if (Method{Name: "flickr"}).Namespace() != "" {
		t.Fatal("a name without a namespace")
	}
}
