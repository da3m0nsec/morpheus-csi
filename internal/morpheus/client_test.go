package morpheus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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
	if len(volumes) != 2 {
		t.Fatalf("expected resize payload to contain existing root plus new volume, got %d", len(volumes))
	}
	rootVolume := volumes[0].(map[string]any)
	if rootVolume["id"] != "test-root-volume-never-real" {
		t.Fatalf("expected existing root volume to be preserved, got %#v", rootVolume["id"])
	}
	newVolume := volumes[1].(map[string]any)
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

func TestMoveVolumeDetachesSourceThenAttachesExistingVolumeToTarget(t *testing.T) {
	var calls []string
	sourceVolumes := []map[string]any{
		{"id": "root-source", "name": "root", "sizeGiB": 80, "rootVolume": true},
		{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 14, "rootVolume": false, "storageType": 38, "datastoreId": 40, "controllerMountPoint": "2224:0:4:1", "device": "sdb"},
	}
	targetVolumes := []map[string]any{
		{"id": "root-target", "name": "root", "sizeGiB": 80, "rootVolume": true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/servers/source-server-never-real":
			_ = json.NewEncoder(w).Encode(serverResponseWithID("source-server-never-real", sourceVolumes))
		case r.Method == http.MethodGet && r.URL.Path == "/api/servers/target-server-never-real":
			_ = json.NewEncoder(w).Encode(serverResponseWithID("target-server-never-real", targetVolumes))
		case r.Method == http.MethodPut && r.URL.Path == "/api/servers/source-server-never-real/volumes/test-volume-never-real/detach":
			calls = append(calls, "detach-source")
			sourceVolumes = []map[string]any{
				{"id": "root-source", "name": "root", "sizeGiB": 80, "rootVolume": true},
			}
		case r.Method == http.MethodPut && r.URL.Path == "/api/servers/target-server-never-real/volumes/test-volume-never-real/attach":
			if len(calls) != 1 || calls[0] != "detach-source" {
				t.Fatalf("attach should run after source detach, got calls %#v", calls)
			}
			calls = append(calls, "attach-target")
			targetVolumes = []map[string]any{
				{"id": "root-target", "name": "root", "sizeGiB": 80, "rootVolume": true},
				{"id": "test-volume-never-real", "name": "pvc-123", "sizeGiB": 14, "rootVolume": false, "storageType": 38, "datastoreId": 40},
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
	volume, err := client.MoveVolume(context.Background(), MoveVolumeRequest{
		VolumeID:           "test-volume-never-real",
		SourceServerID:     "source-server-never-real",
		TargetServerID:     "target-server-never-real",
		CandidateServerIDs: []string{"source-server-never-real", "target-server-never-real"},
	})
	if err != nil {
		t.Fatalf("MoveVolume returned error: %v", err)
	}
	if volume.ID != "test-volume-never-real" {
		t.Fatalf("expected moved volume ID, got %q", volume.ID)
	}
	if len(calls) != 2 || calls[0] != "detach-source" || calls[1] != "attach-target" {
		t.Fatalf("expected detach then attach calls, got %#v", calls)
	}
}

func TestResolveServerIDByNodeNameReturnsExactMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/servers" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("phrase"); got != "hks-cluster-worker-1" {
			t.Fatalf("expected phrase hks-cluster-worker-1, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"servers": []map[string]any{
				{"id": "server-1-never-real", "name": "other-worker"},
				{"id": "server-2-never-real", "name": "hks-cluster-worker-1"},
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	serverID, err := client.ResolveServerIDByNodeName(context.Background(), "hks-cluster-worker-1")
	if err != nil {
		t.Fatalf("ResolveServerIDByNodeName returned error: %v", err)
	}
	if serverID != "server-2-never-real" {
		t.Fatalf("expected server-2-never-real, got %q", serverID)
	}
}

func TestResolveServerIDByNodeNameRejectsAmbiguousMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/servers" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"servers": []map[string]any{
				{"id": "server-1-never-real", "name": "worker-prefix-a"},
				{"id": "server-2-never-real", "name": "worker-prefix-b"},
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.ResolveServerIDByNodeName(context.Background(), "worker-prefix"); err == nil {
		t.Fatal("expected ambiguous node lookup to fail")
	}
}

func TestEnsureVolumeSerializesConcurrentResizesOnSameServer(t *testing.T) {
	// The resize endpoint replaces the server's whole volume array. Without
	// per-server serialization, two concurrent creates both read the initial
	// state and the second PUT clobbers the first volume. This stateful fake
	// adds latency between read and write to widen that race window.
	var mu sync.Mutex
	volumes := []map[string]any{
		{"id": "root", "name": "root", "sizeGiB": 20, "rootVolume": true},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/servers/test-server-never-real":
			mu.Lock()
			snapshot := append([]map[string]any(nil), volumes...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(serverResponse(snapshot))
		case r.Method == http.MethodPut && r.URL.Path == "/api/servers/test-server-never-real/resize":
			var payload struct {
				Volumes []map[string]any `json:"volumes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode resize payload: %v", err)
			}
			// Simulate non-atomic apply latency on the Morpheus side.
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			next := make([]map[string]any, 0, len(payload.Volumes))
			for _, v := range payload.Volumes {
				name, _ := v["name"].(string)
				id := v["id"]
				if idf, ok := id.(float64); ok && idf == -1 {
					id = "vol-" + name
				}
				next = append(next, map[string]any{
					"id":         id,
					"name":       name,
					"sizeGiB":    v["size"],
					"rootVolume": v["rootVolume"],
				})
			}
			volumes = next
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	names := []string{"pvc-a", "pvc-b"}
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			_, errs[i] = client.EnsureVolume(context.Background(), ResizeVolumeRequest{
				Name:    name,
				SizeGiB: 10,
				StorageClass: map[string]string{
					ParamServerID: "test-server-never-real",
				},
			})
		}(i, name)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureVolume(%q) returned error: %v", names[i], err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(volumes) != 3 {
		t.Fatalf("expected root plus both new volumes to survive (3), got %d: %#v", len(volumes), volumes)
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

func TestParseVolumeNormalizesLinuxDeviceName(t *testing.T) {
	volume := parseVolume(map[string]any{
		"id":     "test-volume-never-real",
		"name":   "pvc-123",
		"device": "sdb",
	})
	if volume == nil {
		t.Fatal("expected volume to parse")
	}
	if volume.DevicePath != "/dev/sdb" {
		t.Fatalf("expected /dev/sdb, got %q", volume.DevicePath)
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
	return serverResponseWithID("test-server-never-real", volumes)
}

func serverResponseWithID(id string, volumes []map[string]any) map[string]any {
	return map[string]any{
		"server": map[string]any{
			"id":      id,
			"volumes": volumes,
		},
	}
}
