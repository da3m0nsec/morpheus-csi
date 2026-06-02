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
		case r.Method == http.MethodGet && r.URL.Path == "/api/servers/test-server-never-real":
			_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
				{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/servers/test-server-never-real/resize":
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
			ParamServerID: "test-server-never-real",
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/servers/test-server-never-real":
			gets++
			if gets == 1 {
				_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
					{"id": "test-root-volume-never-real", "name": "root", "size": 20, "rootVolume": true, "storageType": 1, "datastoreId": 40, "controllerMountPoint": "2224:0:4:0"},
				}))
				return
			}
			_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
				{"id": "test-root-volume-never-real", "name": "root", "size": 20, "rootVolume": true, "storageType": 1, "datastoreId": 40, "controllerMountPoint": "2224:0:4:0"},
				{"id": "test-volume-never-real", "name": "pvc-123", "size": 10, "rootVolume": false, "storageType": 38, "datastoreId": 40},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/servers/test-server-never-real/resize":
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
			ParamServerID:    "test-server-never-real",
			ParamStorageType: "38",
			ParamDatastoreID: "40",
		},
	})
	if err != nil {
		t.Fatalf("EnsureVolume returned error: %v", err)
	}
	if volume.ID != "test-volume-never-real" {
		t.Fatalf("expected new sentinel volume id, got %q", volume.ID)
	}
	if _, ok := resizePayload["server"]; ok {
		t.Fatal("resize payload should not include server")
	}
	volumes := resizePayload["volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("expected resize payload to contain only the new volume, got %d", len(volumes))
	}
	newVolume := volumes[0].(map[string]any)
	if newVolume["id"] != float64(-1) {
		t.Fatalf("expected new volume id -1, got %#v", newVolume["id"])
	}
	if newVolume["size"] != float64(10) {
		t.Fatalf("expected size to be 10GiB, got %#v", newVolume["size"])
	}
	if newVolume["storageType"] != float64(38) {
		t.Fatalf("expected storageType 38, got %#v", newVolume["storageType"])
	}
	if newVolume["datastoreId"] != float64(40) {
		t.Fatalf("expected datastoreId 40, got %#v", newVolume["datastoreId"])
	}
}

func TestEnsureVolumeFallsBackToNewVolumeWhenNameIsNotReturned(t *testing.T) {
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/servers/test-server-never-real":
			gets++
			if gets == 1 {
				_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
					{"id": "test-root-volume-never-real", "name": "root", "sizeGiB": 20, "rootVolume": true},
				}))
				return
			}
			_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
				{"id": "test-root-volume-never-real", "name": "root", "sizeGiB": 20, "rootVolume": true},
				{"id": "test-volume-never-real", "name": "Hard Disk 2", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/servers/test-server-never-real/resize":
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
			ParamServerID: "test-server-never-real",
		},
	})
	if err != nil {
		t.Fatalf("EnsureVolume returned error: %v", err)
	}
	if volume.ID != "test-volume-never-real" {
		t.Fatalf("expected fallback sentinel volume id, got %q", volume.ID)
	}
}

func TestDeleteVolumeRemovesNonRootVolumeViaResize(t *testing.T) {
	var resizePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/servers/test-server-never-real":
			_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
				{"id": "test-root-volume-never-real", "name": "root", "sizeGiB": 20, "rootVolume": true},
				{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
			}))
		case r.Method == http.MethodPut && r.URL.Path == "/api/servers/test-server-never-real/resize":
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
	if err := client.DeleteVolume(context.Background(), VolumeRef{ServerID: "test-server-never-real", VolumeID: "test-volume-never-real"}); err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	volumes := resizePayload["volumes"].([]any)
	if len(volumes) != 1 {
		t.Fatalf("expected only root volume to remain, got %d volumes", len(volumes))
	}
}

func TestExpandVolumeRejectsShrink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/servers/test-server-never-real" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
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
			ParamServerID: "test-server-never-real",
		},
	})
	if err == nil {
		t.Fatal("expected shrink to be rejected")
	}
}

func TestParseVolumeConvertsMaxStorageBytesToGiB(t *testing.T) {
	volume := parseVolume(map[string]any{
		"id":         "test-volume-never-real",
		"name":       "pvc-123",
		"maxStorage": float64(10 * 1024 * 1024 * 1024),
	})
	if volume == nil {
		t.Fatal("expected volume to parse")
	}
	if volume.SizeGiB != 10 {
		t.Fatalf("expected 10GiB, got %dGiB", volume.SizeGiB)
	}
}

func TestNewClientWithTLSAllowsSelfSignedWhenInsecure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/servers/test-server-never-real" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(serverResponse([]map[string]any{
			{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 10, "rootVolume": false},
		}))
	}))
	defer server.Close()

	client, err := NewClientWithTLS(server.URL, "token", "", true)
	if err != nil {
		t.Fatalf("NewClientWithTLS returned error: %v", err)
	}
	volume, err := client.GetVolume(context.Background(), VolumeRef{
		ServerID: "test-server-never-real",
		VolumeID: "test-volume-never-real",
	})
	if err != nil {
		t.Fatalf("GetVolume returned error: %v", err)
	}
	if volume.ID != "test-volume-never-real" {
		t.Fatalf("expected sentinel volume id, got %q", volume.ID)
	}
}

func serverResponse(volumes []map[string]any) map[string]any {
	return map[string]any{
		"server": map[string]any{
			"id":      "test-server-never-real",
			"volumes": volumes,
		},
	}
}
