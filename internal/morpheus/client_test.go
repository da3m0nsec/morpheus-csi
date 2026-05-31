package morpheus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnsureVolumeReturnsExistingCompatibleVolume(t *testing.T) {
	var resizeCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("expected bearer token auth header, got %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/instances/7":
			_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
				{"id": 42, "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/instances/7/resize":
			resizeCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	volume, err := client.EnsureVolume(context.Background(), ResizeVolumeRequest{
		Name:    "pvc-123",
		SizeGiB: 10,
		StorageClass: map[string]string{
			ParamInstanceID: "7",
		},
	})
	if err != nil {
		t.Fatalf("EnsureVolume returned error: %v", err)
	}
	if volume.ID != "42" {
		t.Fatalf("expected existing volume id 42, got %q", volume.ID)
	}
	if resizeCalled {
		t.Fatal("EnsureVolume should not resize when a compatible volume already exists")
	}
}

func TestEnsureVolumeAddsMissingVolumeViaResize(t *testing.T) {
	var resizePayload map[string]any
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/instances/7":
			gets++
			if gets == 1 {
				_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
					{"id": 1, "name": "root", "sizeGiB": 20, "rootVolume": true},
				}))
				return
			}
			_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
				{"id": 1, "name": "root", "sizeGiB": 20, "rootVolume": true},
				{"id": 42, "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/instances/7/resize":
			if err := json.NewDecoder(r.Body).Decode(&resizePayload); err != nil {
				t.Fatalf("decode resize payload: %v", err)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	volume, err := client.EnsureVolume(context.Background(), ResizeVolumeRequest{
		Name:    "pvc-123",
		SizeGiB: 10,
		StorageClass: map[string]string{
			ParamInstanceID:    "7",
			ParamStorageTypeID: "5",
		},
	})
	if err != nil {
		t.Fatalf("EnsureVolume returned error: %v", err)
	}
	if volume.ID != "42" {
		t.Fatalf("expected new volume id 42, got %q", volume.ID)
	}
	volumes := resizePayload["instance"].(map[string]any)["volumes"].([]any)
	if len(volumes) != 2 {
		t.Fatalf("expected resize payload to contain root plus new volume, got %d", len(volumes))
	}
}

func TestDeleteVolumeRemovesNonRootVolumeViaResize(t *testing.T) {
	var resizePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/instances/7":
			_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
				{"id": 1, "name": "root", "sizeGiB": 20, "rootVolume": true},
				{"id": 42, "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/instances/7/resize":
			if err := json.NewDecoder(r.Body).Decode(&resizePayload); err != nil {
				t.Fatalf("decode resize payload: %v", err)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.DeleteVolume(context.Background(), VolumeRef{InstanceID: "7", VolumeID: "42"}); err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	volumes := resizePayload["instance"].(map[string]any)["volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("expected only root volume to remain, got %d volumes", len(volumes))
	}
}

func TestExpandVolumeRejectsShrink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/instances/7" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
			{"id": 42, "name": "pvc-123", "sizeGiB": 20, "rootVolume": false},
		}))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.ExpandVolume(context.Background(), ResizeVolumeRequest{
		VolumeID: "42",
		SizeGiB:  10,
		StorageClass: map[string]string{
			ParamInstanceID: "7",
		},
	})
	if err == nil {
		t.Fatal("expected shrink to be rejected")
	}
}

func instanceResponse(volumes []map[string]any) map[string]any {
	return map[string]any{
		"instance": map[string]any{
			"id":      7,
			"volumes": volumes,
		},
	}
}
