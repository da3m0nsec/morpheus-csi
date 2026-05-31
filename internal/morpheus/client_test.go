package morpheus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateStorageVolumeReturnsExistingCompatibleVolume(t *testing.T) {
	var postCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("expected bearer token auth header, got %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/storage-volumes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"storageVolumes": []map[string]any{
					{
						"id":                42,
						"name":              "pvc-123",
						"sizeGiB":           10,
						"storageServerId":   12,
						"storageVolumeType": map[string]any{"id": 4},
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/storage-volumes":
			postCalled = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	volume, err := client.CreateStorageVolume(context.Background(), CreateStorageVolumeRequest{
		Name:    "pvc-123",
		SizeGiB: 10,
		StorageClass: map[string]string{
			ParamStorageServerID:     "12",
			ParamStorageVolumeTypeID: "4",
		},
	})
	if err != nil {
		t.Fatalf("CreateStorageVolume returned error: %v", err)
	}
	if volume.ID != "42" {
		t.Fatalf("expected existing volume id 42, got %q", volume.ID)
	}
	if postCalled {
		t.Fatal("CreateStorageVolume should not POST when a compatible volume already exists")
	}
}

func TestDeleteStorageVolumeTreatsNotFoundAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/storage-volumes/42" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.DeleteStorageVolume(context.Background(), "42"); err != nil {
		t.Fatalf("DeleteStorageVolume returned error: %v", err)
	}
}

func TestResolveServerIDForNodeRequiresUniqueExactMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/storage-servers" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"storageServers": []map[string]any{
				{"id": 7, "name": "worker-1"},
				{"id": 8, "name": "worker-2"},
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	serverID, err := client.ResolveServerIDForNode(context.Background(), "worker-1")
	if err != nil {
		t.Fatalf("ResolveServerIDForNode returned error: %v", err)
	}
	if serverID != "7" {
		t.Fatalf("expected server id 7, got %q", serverID)
	}
}
