package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/localfs"
)

type fakeFiles struct {
	uploaded string
	fields   url.Values
	replaced string
	content  string
	source   string
	fetches  int
	fetchErr error
	sizeErr  error
}

func (f *fakeFiles) Upload(_ context.Context, path string, fields url.Values) (flickr.UploadResult, error) {
	f.uploaded, f.fields = path, fields
	return flickr.UploadResult{PhotoID: "99"}, nil
}

func (f *fakeFiles) Replace(_ context.Context, path, photoID string, fields url.Values) (flickr.UploadResult, error) {
	f.replaced, f.fields = path+"#"+photoID, fields
	return flickr.UploadResult{PhotoID: photoID}, nil
}

func (f *fakeFiles) PickSize(_ context.Context, photoID, label string) (flickr.Size, error) {
	if f.sizeErr != nil {
		return flickr.Size{}, f.sizeErr
	}
	source := f.source
	if source == "" {
		source = "https://live.staticflickr.com/65535/" + photoID + "_abc123_b.jpg"
	}
	return flickr.Size{Label: label, Source: source}, nil
}

func (f *fakeFiles) Fetch(_ context.Context, _ string, w io.Writer) (int64, error) {
	f.fetches++
	n, _ := io.WriteString(w, f.content)
	return int64(n), f.fetchErr
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
		{FileOptions{DownloadDir: &d}, 3},
		{FileOptions{UploadDir: &d, DownloadDir: &d, MaxPerms: flickr.PermsWrite, Authorized: true}, 5},
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

func TestFileToolArgumentErrors(t *testing.T) {
	d := openDir(t)
	if err := os.WriteFile(filepath.Join(d.Root(), "a.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, _ := fileSession(t, &fakeFiles{}, FileOptions{UploadDir: &d, DownloadDir: &d, MaxPerms: flickr.PermsWrite, Authorized: true})
	for name, args := range map[string]map[string]any{
		"flickr_upload":   {"title": "no path"},
		"flickr_replace":  {"path": "a.jpg"},
		"flickr_download": {"path": "x.jpg"},
	} {
		if _, isErr := call(t, cs, name, args); !isErr {
			t.Errorf("%s without its required arguments must fail", name)
		}
	}
	if _, isErr := call(t, cs, "flickr_replace", map[string]any{"path": "../a.jpg", "photo_id": "1"}); !isErr {
		t.Error("replace outside the directory must fail")
	}
}

func TestAFailedDownloadLeavesNoFile(t *testing.T) {
	d := openDir(t)
	cs, _ := fileSession(t, &fakeFiles{content: "PART", fetchErr: errors.New("connection reset")}, FileOptions{DownloadDir: &d})
	for _, args := range []map[string]any{{"photo_id": "1", "path": "x.jpg"}, {"photo_id": "1"}} {
		text, isErr := call(t, cs, "flickr_download", args)
		if !isErr || !strings.Contains(text, "connection reset") {
			t.Fatalf("result = %s", text)
		}
	}
	for _, name := range []string{"x.jpg", "1_abc123_large.jpg"} {
		if _, err := os.Stat(filepath.Join(d.Root(), name)); !os.IsNotExist(err) {
			t.Fatalf("a half-written %s must be removed", name)
		}
	}
}

func TestUploadErrorsReachTheClient(t *testing.T) {
	if res, _ := jsonResult(nil, errors.New("quota exceeded")); !res.IsError {
		t.Fatal("an error must become a tool error")
	}
}

func result(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("not JSON: %s", text)
	}
	return m
}

// Without a path, a download is named <id>_<secret>_<size>.<ext> in the
// download directory and served from there next time.
func TestDownloadUsesTheCache(t *testing.T) {
	d := openDir(t)
	fake := &fakeFiles{content: "IMAGE"}
	cs, _ := fileSession(t, fake, FileOptions{DownloadDir: &d, CacheWarnBytes: 1 << 30})

	text, isErr := call(t, cs, "flickr_download", map[string]any{"photo_id": "55"})
	if isErr {
		t.Fatalf("download: %s", text)
	}
	first := result(t, text)
	want := filepath.Join(d.Root(), "55_abc123_large.jpg")
	if first["path"] != want || first["cached"] != false || fake.fetches != 1 {
		t.Fatalf("first = %v, fetches = %d", first, fake.fetches)
	}
	usage := first["cache"].(map[string]any)
	if usage["bytes"] != float64(5) || usage["files"] != float64(1) || usage["human"] != "5 B" {
		t.Fatalf("cache usage = %v", usage)
	}
	if _, warned := first["warning"]; warned {
		t.Fatal("no warning below the limit")
	}

	second := result(t, mustCall(t, cs, "flickr_download", map[string]any{"photo_id": "55"}))
	if second["path"] != want || second["cached"] != true || fake.fetches != 1 {
		t.Fatalf("second = %v, fetches = %d", second, fake.fetches)
	}

	// A new secret (the photo was replaced or its privacy changed) is a new file.
	fake.source = "https://live.staticflickr.com/65535/55_def456_b.jpg"
	third := result(t, mustCall(t, cs, "flickr_download", map[string]any{"photo_id": "55"}))
	if third["cached"] != false || fake.fetches != 2 || third["path"] != filepath.Join(d.Root(), "55_def456_large.jpg") {
		t.Fatalf("third = %v", third)
	}
}

func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s: %s", name, text)
	}
	return text
}

func TestDownloadWarnsWhenTheCacheIsLarge(t *testing.T) {
	d := openDir(t)
	fake := &fakeFiles{content: "IMAGE"}
	cs, _ := fileSession(t, fake, FileOptions{DownloadDir: &d, CacheWarnBytes: 8})
	mustCall(t, cs, "flickr_download", map[string]any{"photo_id": "1"})
	res := result(t, mustCall(t, cs, "flickr_download", map[string]any{"photo_id": "2"}))
	warning, _ := res["warning"].(string)
	if !strings.Contains(warning, "consider removing old images") {
		t.Fatalf("warning = %q", warning)
	}
	candidates := res["oldest"].([]any)
	if len(candidates) == 0 || candidates[0].(map[string]any)["name"] != "1_abc123_large.jpg" {
		t.Fatalf("candidates = %v", candidates)
	}
}

func TestDownloadRefusesNonImageSources(t *testing.T) {
	d := openDir(t)
	cs, _ := fileSession(t, &fakeFiles{source: "https://www.flickr.com/photos/x/1/play/site/abc/"}, FileOptions{DownloadDir: &d})
	if text, isErr := call(t, cs, "flickr_download", map[string]any{"photo_id": "1", "size": "Site MP4"}); !isErr {
		t.Fatalf("a video page is not an image to cache: %s", text)
	}
}

func cacheFile(t *testing.T, d localfs.Dir, name string, size int, age time.Duration) {
	t.Helper()
	p := filepath.Join(d.Root(), name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestCacheStatusAndPurge(t *testing.T) {
	d := openDir(t)
	cacheFile(t, d, "1_aa_large.jpg", 100, time.Hour)
	cacheFile(t, d, "2_bb_large.jpg", 200, 72*time.Hour)
	cacheFile(t, d, "3_cc_small.jpg", 50, 24*time.Hour)
	cacheFile(t, d, "keep-me.jpg", 999, 1000*time.Hour) // not a cache file
	cs, _ := fileSession(t, &fakeFiles{}, FileOptions{DownloadDir: &d})

	status := result(t, mustCall(t, cs, "flickr_cache_status", map[string]any{"limit": 2}))
	if status["bytes"] != float64(350) || status["files"] != float64(3) {
		t.Fatalf("status = %v", status)
	}
	if entries := status["oldest"].([]any); len(entries) != 2 || entries[0].(map[string]any)["name"] != "2_bb_large.jpg" {
		t.Fatalf("oldest = %v", entries)
	}

	// Dry run by default: the plan, nothing removed.
	plan := result(t, mustCall(t, cs, "flickr_cache_purge", map[string]any{"target_bytes": 100}))
	if plan["dry_run"] != true || plan["bytes"] != float64(250) || len(plan["files"].([]any)) != 2 {
		t.Fatalf("plan = %v", plan)
	}
	if _, err := os.Stat(filepath.Join(d.Root(), "2_bb_large.jpg")); err != nil {
		t.Fatal("a dry run must not remove anything")
	}

	done := result(t, mustCall(t, cs, "flickr_cache_purge", map[string]any{"older_than_days": 2, "dry_run": false}))
	if done["dry_run"] != false || done["bytes"] != float64(200) {
		t.Fatalf("purge = %v", done)
	}
	if _, err := os.Stat(filepath.Join(d.Root(), "2_bb_large.jpg")); !os.IsNotExist(err) {
		t.Fatal("the old file must be gone")
	}
	if _, err := os.Stat(filepath.Join(d.Root(), "keep-me.jpg")); err != nil {
		t.Fatal("files outside the naming convention are never touched")
	}
	after := done["cache"].(map[string]any)
	if after["bytes"] != float64(150) || after["files"] != float64(2) {
		t.Fatalf("after = %v", after)
	}

	for _, args := range []map[string]any{{}, {"target_bytes": 1, "older_than_days": 1}, {"target_bytes": -1}, {"older_than_days": "x"}} {
		if _, isErr := call(t, cs, "flickr_cache_purge", args); !isErr {
			t.Errorf("purge %v must fail: exactly one non-negative criterion", args)
		}
	}
}

func TestCacheToolErrors(t *testing.T) {
	d := openDir(t)
	cs, _ := fileSession(t, &fakeFiles{sizeErr: errors.New("photo not found")}, FileOptions{DownloadDir: &d})
	for name, args := range map[string]map[string]any{
		"flickr_download":     {"photo_id": "1"},
		"flickr_cache_status": {"limit": -1},
		"flickr_cache_purge":  {"bogus": 1},
	} {
		if _, isErr := call(t, cs, name, args); !isErr {
			t.Errorf("%s %v must fail", name, args)
		}
	}
	if _, isErr := call(t, cs, "flickr_cache_status", map[string]any{"bogus": 1}); !isErr {
		t.Error("an unknown argument must fail")
	}

	// Nothing to remove is an empty list, not null.
	none := result(t, mustCall(t, cs, "flickr_cache_purge", map[string]any{"target_bytes": 1 << 40}))
	if files, ok := none["files"].([]any); !ok || len(files) != 0 {
		t.Fatalf("files = %v", none["files"])
	}

	if err := os.Remove(d.Root()); err != nil {
		t.Fatal(err)
	}
	if _, isErr := call(t, cs, "flickr_cache_status", nil); !isErr {
		t.Error("status on a missing directory must fail")
	}
	if _, isErr := call(t, cs, "flickr_cache_purge", map[string]any{"older_than_days": 1}); !isErr {
		t.Error("purge on a missing directory must fail")
	}
}

func TestPurgeReportsFilesItCannotRemove(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a directory the user cannot write to")
	}
	d := openDir(t)
	cacheFile(t, d, "1_aa_large.jpg", 10, 72*time.Hour)
	if err := os.Chmod(d.Root(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.Root(), 0o700) })
	cs, _ := fileSession(t, &fakeFiles{}, FileOptions{DownloadDir: &d})
	if _, isErr := call(t, cs, "flickr_cache_purge", map[string]any{"target_bytes": 0, "dry_run": false}); !isErr {
		t.Fatal("a file that cannot be removed must be reported")
	}
}
