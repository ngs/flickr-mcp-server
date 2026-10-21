package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect starts srv on an in-memory transport and returns a client session.
func connect(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestInitializeReportsNameAndVersion(t *testing.T) {
	session := connect(t, New())
	info := session.InitializeResult().ServerInfo
	if info.Name != ServerName || info.Version != VERSION {
		t.Fatalf("server info = %s %s, want %s %s", info.Name, info.Version, ServerName, VERSION)
	}
}

func TestNoToolsWithoutRegistration(t *testing.T) {
	session := connect(t, New())
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) != 0 {
		t.Fatalf("got %d tools, want 0", len(res.Tools))
	}
}
