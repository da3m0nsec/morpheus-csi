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
		case r.Method == http.MethodGet && r.URL.Path == "/api/instances/test-instance-never-real":
			_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
				{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/instances/test-instance-never-real/resize":
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
			ParamInstanceID: "test-instance-never-real",
		},
	})
	if err != nil {
		t.Fatalf("EnsureVolume returned error: %v", err)
	}
	if volume.ID != "test-volume-never-real" {
		t.Fatalf("expected existing sentinel volume id, got %q", volume.ID)
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/instances/test-instance-never-real":
			gets++
			if gets == 1 {
				_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
					{"id": "test-root-volume-never-real", "name": "root", "sizeGiB": 20, "rootVolume": true},
				}))
				return
			}
			_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
				{"id": "test-root-volume-never-real", "name": "root", "sizeGiB": 20, "rootVolume": true},
				{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/instances/test-instance-never-real/resize":
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
			ParamInstanceID:    "test-instance-never-real",
			ParamStorageTypeID: "5",
		},
	})
	if err != nil {
		t.Fatalf("EnsureVolume returned error: %v", err)
	}
	if volume.ID != "test-volume-never-real" {
		t.Fatalf("expected new sentinel volume id, got %q", volume.ID)
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/instances/test-instance-never-real":
			_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
				{"id": "test-root-volume-never-real", "name": "root", "sizeGiB": 20, "rootVolume": true},
				{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/instances/test-instance-never-real/resize":
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
	if err := client.DeleteVolume(context.Background(), VolumeRef{InstanceID: "test-instance-never-real", VolumeID: "test-volume-never-real"}); err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	volumes := resizePayload["instance"].(map[string]any)["volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("expected only root volume to remain, got %d volumes", len(volumes))
	}
}

func TestExpandVolumeRejectsShrink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/instances/test-instance-never-real" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
			{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 20, "rootVolume": false},
		}))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	_, err = client.ExpandVolume(context.Background(), ResizeVolumeRequest{
		VolumeID: "test-volume-never-real",
		SizeGiB:  10,
		StorageClass: map[string]string{
			ParamInstanceID: "test-instance-never-real",
		},
	})
	if err == nil {
		t.Fatal("expected shrink to be rejected")
	}
}

func TestNewClientWithTLSAllowsSelfSignedWhenInsecure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/instances/test-instance-never-real" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(instanceResponse([]map[string]any{
			{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
		}))
	}))
	defer server.Close()

	client, err := NewClientWithTLS(server.URL, "token", "", true)
	if err != nil {
		t.Fatalf("NewClientWithTLS returned error: %v", err)
	}
	volume, err := client.GetVolume(context.Background(), VolumeRef{
		InstanceID: "test-instance-never-real",
		VolumeID:   "test-volume-never-real",
	})
	if err != nil {
		t.Fatalf("GetVolume returned error: %v", err)
	}
	if volume.ID != "test-volume-never-real" {
		t.Fatalf("expected sentinel volume id, got %q", volume.ID)
	}
}

func instanceResponse(volumes []map[string]any) map[string]any {
	return map[string]any{
		"instance": map[string]any{
			"id":      "test-instance-never-real",
			"volumes": volumes,
		},
	}
}
