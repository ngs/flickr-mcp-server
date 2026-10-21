package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/cache"
	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/localfs"
)

// FileClient is the part of flickr.Client the file tools need.
type FileClient interface {
	Upload(ctx context.Context, path string, fields url.Values) (flickr.UploadResult, error)
	Replace(ctx context.Context, path, photoID string, fields url.Values) (flickr.UploadResult, error)
	PickSize(ctx context.Context, photoID, label string) (flickr.Size, error)
	Fetch(ctx context.Context, source string, w io.Writer) (int64, error)
}

// FileOptions say where the file tools may read and write. A nil directory
// leaves the corresponding tools out.
type FileOptions struct {
	UploadDir   *localfs.Dir
	DownloadDir *localfs.Dir
	// MaxPerms and Authorized as in Options: upload and replace need write
	// permission; download works without a token for public photos.
	MaxPerms   flickr.Perms
	Authorized bool
	// CacheWarnBytes is the cache size above which downloads suggest
	// removing old images. 0 means DefaultCacheWarnBytes.
	CacheWarnBytes int64
}

// DefaultCacheWarnBytes is 5 GiB.
const DefaultCacheWarnBytes int64 = 5 << 30

// oldestShown is how many removal candidates a warning lists.
const oldestShown = 5

var uploadFields = []Argument{
	{Name: "title", Optional: true, Description: "The title of the photo."},
	{Name: "description", Optional: true, Description: "A description of the photo. May contain some limited HTML."},
	{Name: "tags", Optional: true, Description: "A space-separated list of tags. Put multi-word tags in double quotes."},
	{Name: "is_public", Optional: true, Description: "Set to 0 for no, 1 for yes. Specifies who can view the photo."},
	{Name: "is_friend", Optional: true, Description: "Set to 0 for no, 1 for yes."},
	{Name: "is_family", Optional: true, Description: "Set to 0 for no, 1 for yes."},
	{Name: "safety_level", Optional: true, Description: "1 for Safe, 2 for Moderate, 3 for Restricted."},
	{Name: "content_type", Optional: true, Description: "1 for Photo, 2 for Screenshot, 3 for Other."},
	{Name: "hidden", Optional: true, Description: "1 to keep the photo in global search results, 2 to hide it."},
	{Name: "async", Optional: true, Description: "1 to upload asynchronously: the result is a ticket id to check with flickr_photos_upload_checkTickets."},
}

// RegisterFiles adds flickr_upload, flickr_replace and flickr_download as
// the options allow, and returns how many were added.
func RegisterFiles(srv *mcp.Server, c FileClient, opts FileOptions) int {
	n := 0
	canWrite := opts.Authorized && opts.MaxPerms >= flickr.PermsWrite
	if opts.UploadDir != nil && canWrite {
		dir := *opts.UploadDir
		upload := Method{
			Name:        "flickr.upload",
			Description: "Upload a photo or video from " + dir.Root() + " to Flickr. Returns the new photo id (or a ticket id with async=1).",
			Arguments:   append([]Argument{{Name: "path", Description: "The file to upload, relative to " + dir.Root() + "."}}, uploadFields...),
		}
		srv.AddTool(fileTool(upload, flickr.PermsWrite), func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			params, err := buildParams(upload, req.Params.Arguments)
			if err != nil {
				return errorResult(err), nil
			}
			path, err := dir.ReadPath(params.Get("path"))
			if err != nil {
				return errorResult(err), nil
			}
			params.Del("path")
			res, err := c.Upload(ctx, path, params)
			return jsonResult(res, err)
		})
		replace := Method{
			Name:        "flickr.replace",
			Description: "Replace the file of an existing photo with a file from " + dir.Root() + ". The photo keeps its id, title, tags and so on.",
			Arguments: []Argument{
				{Name: "path", Description: "The new file, relative to " + dir.Root() + "."},
				{Name: "photo_id", Description: "The id of the photo to replace."},
				{Name: "async", Optional: true, Description: "1 to replace asynchronously."},
			},
		}
		srv.AddTool(fileTool(replace, flickr.PermsWrite), func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			params, err := buildParams(replace, req.Params.Arguments)
			if err != nil {
				return errorResult(err), nil
			}
			path, err := dir.ReadPath(params.Get("path"))
			if err != nil {
				return errorResult(err), nil
			}
			photoID := params.Get("photo_id")
			params.Del("path")
			params.Del("photo_id")
			res, err := c.Replace(ctx, path, photoID, params)
			return jsonResult(res, err)
		})
		n += 2
	}
	if opts.DownloadDir != nil {
		warn := opts.CacheWarnBytes
		if warn <= 0 {
			warn = DefaultCacheWarnBytes
		}
		n += registerDownload(srv, c, *opts.DownloadDir, warn)
	}
	return n
}

func fileTool(m Method, perms flickr.Perms) *mcp.Tool {
	t := toolFor(Method{Name: m.Name, Description: m.Description, Arguments: m.Arguments, RequiredPerms: int(perms), Post: perms >= flickr.PermsWrite})
	t.Description = m.Description
	if perms > flickr.PermsNone {
		t.Description += fmt.Sprintf(" Needs %s permission.", perms)
	}
	return t
}

func jsonResult(v any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return errorResult(err), nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return errorResult(err), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
}

// registerDownload adds flickr_download and the two cache tools.
func registerDownload(srv *mcp.Server, c FileClient, dir localfs.Dir, warn int64) int {
	download := Method{
		Name: "flickr.download",
		Description: "Save a photo to " + dir.Root() + " at one of the sizes flickr.photos.getSizes lists. " +
			"Without path, the file is named <photo id>_<secret>_<size>.<ext> and kept as a cache: a later call for the same photo and size returns it without fetching (cached: true). " +
			"The result reports how much the cache holds; nothing is removed automatically.",
		Arguments: []Argument{
			{Name: "photo_id", Description: "The id of the photo."},
			{Name: "size", Optional: true, Description: "The size label from flickr.photos.getSizes, such as Original, Large 2048, Large or Medium 800. Defaults to Large."},
			{Name: "path", Optional: true, Description: "Save here instead, relative to " + dir.Root() + ". Such files are not part of the cache."},
			{Name: "overwrite", Optional: true, Description: "With path: true to replace an existing file."},
		},
	}
	srv.AddTool(fileTool(download, flickr.PermsNone), func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params, err := buildParams(download, req.Params.Arguments)
		if err != nil {
			return errorResult(err), nil
		}
		label := params.Get("size")
		if label == "" {
			label = "Large"
		}
		size, err := c.PickSize(ctx, params.Get("photo_id"), label)
		if err != nil {
			return errorResult(err), nil
		}
		rel, cacheable := params.Get("path"), false
		if rel == "" {
			if rel, err = cache.Name(params.Get("photo_id"), size.Source, size.Label); err != nil {
				return errorResult(err), nil
			}
			cacheable = true
		}
		res := map[string]any{"size": size.Label, "source": size.Source, "cached": false}
		target := filepath.Join(dir.Root(), rel)
		if info, err := os.Stat(target); cacheable && err == nil && info.Mode().IsRegular() {
			_ = cache.Touch(target)
			res["path"], res["bytes"], res["cached"] = target, info.Size(), true
		} else {
			written, err := save(ctx, c, dir, rel, params.Get("overwrite") == "1" && !cacheable, size.Source)
			if err != nil {
				return errorResult(err), nil
			}
			res["path"], res["bytes"] = target, written
		}
		stats, err := cache.Scan(dir.Root())
		if err != nil {
			return errorResult(err), nil
		}
		res["cache"] = usage(stats)
		if stats.Bytes > warn {
			res["warning"] = fmt.Sprintf("The image cache in %s holds %s, more than %s: consider removing old images with flickr_cache_purge.", dir.Root(), cache.Human(stats.Bytes), cache.Human(warn))
			res["oldest"] = oldest(stats, oldestShown)
		}
		return jsonResult(res, nil)
	})

	status := Method{
		Name:        "flickr.cache.status",
		Description: "How much the image cache in " + dir.Root() + " holds, with the least recently used files first.",
		Arguments:   []Argument{{Name: "limit", Optional: true, Description: "How many files to list. Defaults to 20."}},
	}
	srv.AddTool(fileTool(status, flickr.PermsNone), func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params, err := buildParams(status, req.Params.Arguments)
		if err != nil {
			return errorResult(err), nil
		}
		limit := 20
		if v := params.Get("limit"); v != "" {
			if limit, err = strconv.Atoi(v); err != nil || limit < 0 {
				return errorResult(fmt.Errorf("limit must be a non-negative number")), nil
			}
		}
		stats, err := cache.Scan(dir.Root())
		if err != nil {
			return errorResult(err), nil
		}
		res := usage(stats)
		res["oldest"] = oldest(stats, limit)
		return jsonResult(res, nil)
	})

	purge := Method{
		Name: "flickr.cache.purge",
		Description: "Remove least recently used images from the cache in " + dir.Root() + ", either down to target_bytes or those not used for older_than_days. " +
			"Only files named like the cache (<photo id>_<secret>_<size>.<ext>) are considered. A dry run unless dry_run is false.",
		Arguments: []Argument{
			{Name: "target_bytes", Optional: true, Description: "Remove the oldest files until the cache holds at most this many bytes."},
			{Name: "older_than_days", Optional: true, Description: "Remove files not used for this many days."},
			{Name: "dry_run", Optional: true, Description: "false to really remove the files. Defaults to true: only list them."},
		},
	}
	purgeTool := fileTool(purge, flickr.PermsNone)
	purgeTool.Annotations.ReadOnlyHint = false
	purgeTool.Annotations.DestructiveHint = boolPtr(true)
	srv.AddTool(purgeTool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params, err := buildParams(purge, req.Params.Arguments)
		if err != nil {
			return errorResult(err), nil
		}
		stats, err := cache.Scan(dir.Root())
		if err != nil {
			return errorResult(err), nil
		}
		plan, err := purgePlan(stats, params.Get("target_bytes"), params.Get("older_than_days"))
		if err != nil {
			return errorResult(err), nil
		}
		dryRun := params.Get("dry_run") != "0"
		var bytes int64
		for _, e := range plan {
			bytes += e.Bytes
		}
		if !dryRun {
			if bytes, err = cache.Remove(plan); err != nil {
				return errorResult(err), nil
			}
			if stats, err = cache.Scan(dir.Root()); err != nil {
				return errorResult(err), nil
			}
		}
		return jsonResult(map[string]any{
			"dry_run": dryRun,
			"files":   entriesOrEmpty(plan),
			"bytes":   bytes,
			"human":   cache.Human(bytes),
			"cache":   usage(stats),
		}, nil)
	})
	return 3
}

// save fetches source into rel, removing the file again if the fetch fails.
func save(ctx context.Context, c FileClient, dir localfs.Dir, rel string, overwrite bool, source string) (int64, error) {
	f, target, err := dir.Create(rel, overwrite)
	if err != nil {
		return 0, err
	}
	written, err := c.Fetch(ctx, source, f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(target)
		return 0, err
	}
	return written, nil
}

func purgePlan(stats cache.Stats, targetBytes, olderThanDays string) ([]cache.Entry, error) {
	switch {
	case targetBytes != "" && olderThanDays == "":
		n, err := strconv.ParseInt(targetBytes, 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("target_bytes must be a non-negative whole number")
		}
		return cache.PlanToSize(stats, n), nil
	case olderThanDays != "" && targetBytes == "":
		days, err := strconv.ParseFloat(olderThanDays, 64)
		if err != nil || days < 0 {
			return nil, fmt.Errorf("older_than_days must be a non-negative number")
		}
		return cache.PlanOlderThan(stats, time.Now().Add(-time.Duration(days*24*float64(time.Hour)))), nil
	default:
		return nil, fmt.Errorf("give exactly one of target_bytes and older_than_days")
	}
}

func usage(s cache.Stats) map[string]any {
	return map[string]any{"bytes": s.Bytes, "human": cache.Human(s.Bytes), "files": s.Files}
}

func oldest(s cache.Stats, n int) []cache.Entry {
	if n < len(s.Entries) {
		return s.Entries[:n]
	}
	return entriesOrEmpty(s.Entries)
}

// entriesOrEmpty keeps an empty list as [] in the JSON rather than null.
func entriesOrEmpty(e []cache.Entry) []cache.Entry {
	if e == nil {
		return []cache.Entry{}
	}
	return e
}
