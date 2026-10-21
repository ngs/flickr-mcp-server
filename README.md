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

## License

[MIT](LICENSE)

This product uses the Flickr API but is not endorsed or certified by
SmugMug, Inc.
