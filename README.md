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

## License

[MIT](LICENSE)

This product uses the Flickr API but is not endorsed or certified by
SmugMug, Inc.
