# flickr-mcp-server

An [MCP](https://modelcontextprotocol.io) server that exposes the
[Flickr API](https://www.flickr.com/services/api/) to MCP clients as tools.

> Work in progress. Not released yet.

## Goals

- Every Flickr API method as a tool, generated from the API's own method
  descriptions rather than written by hand.
- Upload and replace, which are not regular API methods, as hand-written tools.
- OAuth 1.0a, with a permission ceiling (`read`, `write` or `delete`) chosen
  when the server starts. Tools that need more than the ceiling are not
  registered at all.

## Setup

1. Create a non-commercial API key in the
   [App Garden](https://www.flickr.com/services/apps/create/).
2. Save the key and the secret. Neither is echoed:

   ```sh
   flickr-mcp-server setup
   ```

   This writes `~/.config/flickr-mcp-server/credentials` (mode 0600; the
   directory follows `$XDG_CONFIG_HOME` when it is set):

   ```sh
   FLICKR_API_KEY=...
   FLICKR_API_SECRET=...
   ```

   The environment variables of the same names override the file. A
   credentials or token file that other users can read is refused.
3. Authorize your Flickr account. The permission you ask for is the most the
   server can ever use:

   ```sh
   flickr-mcp-server auth --perms read   # or write, delete; --open opens the browser
   ```

   Open the printed URL, allow access and paste the code Flickr shows. The
   access token is saved to `~/.config/flickr-mcp-server/token.json` (mode 0600).
4. Check it:

   ```sh
   flickr-mcp-server whoami
   ```

## Tools

Each Flickr API method is a tool named after it, with dots replaced by
underscores: `flickr.photos.search` becomes `flickr_photos_search`. The tool
takes the method's arguments (`api_key` is added by the server), checks the
required ones before calling Flickr, and returns Flickr's JSON response.
Booleans are sent as `1` / `0`.

Which tools exist depends on how the server is started:

| Flag | Default | Meaning |
|---|---|---|
| `--max-perms` | `read` | The highest permission a tool may need (`read`, `write` or `delete`). Tools that need more are not registered. If the saved token has less, its permission is used. |
| `--namespaces` | all | Comma-separated namespaces to expose, such as `photos,photosets,tags`. |

Without an authorized token, only methods that need no login are registered,
and calls are not signed. With a token, every call is signed, so results
include what the account can see. The retired `flickr.auth.*` methods are not
exposed; authorization is done with `flickr-mcp-server auth`.

Example `.mcp.json` for Claude Code:

```json
{
  "mcpServers": {
    "flickr": {
      "command": "flickr-mcp-server",
      "args": ["--max-perms", "write"]
    }
  }
}
```

## The method list

The tools are generated from `api/methods.json`, a snapshot of every API
method with its arguments and the permission it needs:

```sh
flickr-mcp-server snapshot --source docs        # from https://www.flickr.com/services/api/ (no key needed)
flickr-mcp-server snapshot --source reflection  # from flickr.reflection.* (needs an API key)
go generate ./...                               # api/methods.json -> tools/zz_generated.go
```

The current snapshot comes from the reflection API (2026-10-21, 224 methods).
It matched the one built from the documentation pages method for method:
the same names, permissions, POST and signing requirements, arguments and
descriptions. CI fails if `tools/zz_generated.go` does not match the
snapshot.

## License

[MIT](LICENSE)

This product uses the Flickr API but is not endorsed or certified by
SmugMug, Inc.
