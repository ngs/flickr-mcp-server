package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestE2EReadOnly calls read-only tools against the real Flickr API with the
// developer's own credentials and token. It runs only with FLICKR_E2E=1 and
// never calls a tool that changes anything on Flickr.
func TestE2EReadOnly(t *testing.T) {
	if os.Getenv("FLICKR_E2E") != "1" {
		t.Skip("set FLICKR_E2E=1 to call the real Flickr API")
	}
	downloads, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, err := buildServer(serverConfig{maxPerms: 1, maxResponseBytes: 256 * 1024, downloadDir: downloads})
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		for tool, err := range cs.Tools(ctx, nil) {
			if err != nil {
				t.Fatal(err)
			}
			if tool.Name == name && !tool.Annotations.ReadOnlyHint && name != "flickr_download" {
				t.Fatalf("%s is not read-only; the e2e test must not call it", name)
			}
		}
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if res.IsError {
			t.Fatalf("%s: %s", name, text)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}

	login := call("flickr_test_login", nil)
	userID := login["user"].(map[string]any)["id"].(string)
	t.Logf("logged in as %s", userID)

	call("flickr_people_getInfo", map[string]any{"user_id": userID})
	call("flickr_photosets_getList", map[string]any{"user_id": userID, "per_page": 3})
	search := call("flickr_photos_search", map[string]any{"user_id": "me", "per_page": 3})
	photos := search["photos"].(map[string]any)
	if photos["perpage"] != float64(3) {
		t.Fatalf("per_page did not reach Flickr: %v", photos["perpage"])
	}
	list := photos["photo"].([]any)
	if len(list) == 0 {
		t.Skip("the account has no photos; stopping before the per-photo calls")
	}
	photoID := list[0].(map[string]any)["id"].(string)
	call("flickr_photos_getInfo", map[string]any{"photo_id": photoID})
	call("flickr_photos_getSizes", map[string]any{"photo_id": photoID})
	call("flickr_photos_getExif", map[string]any{"photo_id": photoID})

	saved := call("flickr_download", map[string]any{"photo_id": photoID, "path": "e2e/photo.jpg", "size": "Small"})
	if !strings.HasPrefix(saved["path"].(string), downloads) || saved["bytes"].(float64) <= 0 {
		t.Fatalf("download = %v", saved)
	}
}
