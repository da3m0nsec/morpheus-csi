package driver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestKubernetesNodeResolverReadsServerIDLabel(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("test-token"), 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/nodes/worker-2":
			_, _ = w.Write([]byte(`{"metadata":{"labels":{"morpheus.csi/server-id":"896"}}}`))
		case "/api/v1/nodes":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"labels":{"morpheus.csi/server-id":"896"}}}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	resolver, err := newKubernetesNodeServerResolverWithHTTPClient(server.URL, tokenPath, "morpheus.csi/server-id", server.Client())
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	serverID, err := resolver.ServerIDForNode(context.Background(), "worker-2")
	if err != nil {
		t.Fatalf("ServerIDForNode returned error: %v", err)
	}
	if serverID != "896" {
		t.Fatalf("expected server ID 896, got %q", serverID)
	}
	serverIDs, err := resolver.ServerIDs(context.Background())
	if err != nil {
		t.Fatalf("ServerIDs returned error: %v", err)
	}
	if len(serverIDs) != 1 || serverIDs[0] != "896" {
		t.Fatalf("expected one server ID 896, got %#v", serverIDs)
	}
}
