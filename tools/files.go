package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/localfs"
)

// FileClient is the part of flickr.Client the file tools need.
type FileClient interface {
	Upload(ctx context.Context, path string, fields url.Values) (flickr.UploadResult, error)
	Replace(ctx context.Context, path, photoID string, fields url.Values) (flickr.UploadResult, error)
	Download(ctx context.Context, photoID, label string, w io.Writer) (flickr.Size, int64, error)
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
}

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
		dir := *opts.DownloadDir
		download := Method{
			Name:        "flickr.download",
			Description: "Save a photo to " + dir.Root() + " at one of the sizes flickr.photos.getSizes lists. Existing files are kept unless overwrite is set.",
			Arguments: []Argument{
				{Name: "photo_id", Description: "The id of the photo."},
				{Name: "path", Description: "Where to save it, relative to " + dir.Root() + "."},
				{Name: "size", Optional: true, Description: "The size label from flickr.photos.getSizes, such as Original, Large 2048, Large or Medium 800. Defaults to Large."},
				{Name: "overwrite", Optional: true, Description: "true to replace an existing file."},
			},
		}
		srv.AddTool(fileTool(download, flickr.PermsNone), func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			params, err := buildParams(download, req.Params.Arguments)
			if err != nil {
				return errorResult(err), nil
			}
			size := params.Get("size")
			if size == "" {
				size = "Large"
			}
			f, target, err := dir.Create(params.Get("path"), params.Get("overwrite") == "1")
			if err != nil {
				return errorResult(err), nil
			}
			chosen, written, err := c.Download(ctx, params.Get("photo_id"), size, f)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(target)
				return errorResult(err), nil
			}
			return jsonResult(map[string]any{"path": target, "size": chosen.Label, "bytes": written, "source": chosen.Source}, nil)
		})
		n++
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
