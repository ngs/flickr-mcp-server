package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/localfs"
)

type fakeFiles struct {
	uploaded string
	fields   url.Values
	replaced string
	content  string
}

func (f *fakeFiles) Upload(_ context.Context, path string, fields url.Values) (flickr.UploadResult, error) {
	f.uploaded, f.fields = path, fields
	return flickr.UploadResult{PhotoID: "99"}, nil
}

func (f *fakeFiles) Replace(_ context.Context, path, photoID string, fields url.Values) (flickr.UploadResult, error) {
	f.replaced, f.fields = path+"#"+photoID, fields
	return flickr.UploadResult{PhotoID: photoID}, nil
}

func (f *fakeFiles) Download(_ context.Context, _, label string, w io.Writer) (flickr.Size, int64, error) {
	n, err := io.WriteString(w, f.content)
	return flickr.Size{Label: label}, int64(n), err
}

func fileSession(t *testing.T, c FileClient, opts FileOptions) (*mcp.ClientSession, int) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	n := RegisterFiles(srv, c, opts)
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, n
}

func openDir(t *testing.T) localfs.Dir {
	t.Helper()
	d, err := localfs.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFileToolsNeedTheirDirectoriesAndPermission(t *testing.T) {
	d := openDir(t)
	cases := []struct {
		opts FileOptions
		want int
	}{
		{FileOptions{MaxPerms: flickr.PermsDelete, Authorized: true}, 0},
		{FileOptions{UploadDir: &d, MaxPerms: flickr.PermsRead, Authorized: true}, 0},
		{FileOptions{UploadDir: &d, MaxPerms: flickr.PermsWrite}, 0},
		{FileOptions{UploadDir: &d, MaxPerms: flickr.PermsWrite, Authorized: true}, 2},
		{FileOptions{DownloadDir: &d}, 1},
		{FileOptions{UploadDir: &d, DownloadDir: &d, MaxPerms: flickr.PermsWrite, Authorized: true}, 3},
	}
	for i, c := range cases {
		if _, n := fileSession(t, &fakeFiles{}, c.opts); n != c.want {
			t.Errorf("case %d: registered %d, want %d", i, n, c.want)
		}
	}
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestUploadReadsOnlyInsideTheDirectory(t *testing.T) {
	d := openDir(t)
	if err := os.WriteFile(filepath.Join(d.Root(), "a.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeFiles{}
	cs, _ := fileSession(t, fake, FileOptions{UploadDir: &d, MaxPerms: flickr.PermsWrite, Authorized: true})

	text, isErr := call(t, cs, "flickr_upload", map[string]any{"path": "a.jpg", "title": "Sea bass", "is_public": false})
	if isErr || !strings.Contains(text, `"photo_id":"99"`) {
		t.Fatalf("upload: %s", text)
	}
	if fake.uploaded != filepath.Join(d.Root(), "a.jpg") || fake.fields.Get("title") != "Sea bass" || fake.fields.Get("is_public") != "0" || fake.fields.Has("path") {
		t.Fatalf("uploaded %s with %v", fake.uploaded, fake.fields)
	}
	fake.uploaded = ""
	if _, isErr := call(t, cs, "flickr_upload", map[string]any{"path": "../etc/passwd"}); !isErr || fake.uploaded != "" {
		t.Fatal("a path outside the directory must be refused before uploading")
	}

	if text, isErr := call(t, cs, "flickr_replace", map[string]any{"path": "a.jpg", "photo_id": "7"}); isErr {
		t.Fatalf("replace: %s", text)
	}
	if fake.replaced != filepath.Join(d.Root(), "a.jpg")+"#7" || fake.fields.Has("photo_id") {
		t.Fatalf("replaced %s with %v", fake.replaced, fake.fields)
	}
}

func TestDownloadWritesInsideTheDirectory(t *testing.T) {
	d := openDir(t)
	fake := &fakeFiles{content: "IMAGE"}
	cs, _ := fileSession(t, fake, FileOptions{DownloadDir: &d})

	text, isErr := call(t, cs, "flickr_download", map[string]any{"photo_id": "1", "path": "2026/fish.jpg"})
	if isErr {
		t.Fatalf("download: %s", text)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatal(err)
	}
	if res["size"] != "Large" || res["bytes"] != float64(5) {
		t.Fatalf("result = %v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(d.Root(), "2026", "fish.jpg")); string(b) != "IMAGE" {
		t.Fatalf("file = %q", b)
	}
	if _, isErr := call(t, cs, "flickr_download", map[string]any{"photo_id": "1", "path": "2026/fish.jpg"}); !isErr {
		t.Fatal("an existing file must be kept without overwrite")
	}
	if text, isErr := call(t, cs, "flickr_download", map[string]any{"photo_id": "1", "path": "2026/fish.jpg", "overwrite": true, "size": "Original"}); isErr {
		t.Fatalf("overwrite: %s", text)
	}
	if _, isErr := call(t, cs, "flickr_download", map[string]any{"photo_id": "1", "path": "../out.jpg"}); !isErr {
		t.Fatal("a path outside the directory must be refused")
	}
}
