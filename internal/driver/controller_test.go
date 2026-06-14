package driver

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/da3m0nsec/morpheus-csi/internal/config"
	"github.com/da3m0nsec/morpheus-csi/internal/morpheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateVolumeEnsuresServerVolume(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:      "test-volume-never-real",
			Name:    "pvc-123",
			SizeGiB: 10,
		},
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	resp, err := driver.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:          "pvc-123",
		CapacityRange: &csi.CapacityRange{RequiredBytes: 10 * gibibyte},
		Parameters: map[string]string{
			morpheus.ParamServerID:      "test-server-never-real",
			"csi.storage.k8s.io/fstype": "ext4",
		},
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability()},
	})
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if resp.GetVolume().GetVolumeId() != "test-server-never-real:test-volume-never-real" {
		t.Fatalf("expected encoded volume id test-server-never-real:test-volume-never-real, got %q", resp.GetVolume().GetVolumeId())
	}
	if fake.ensureRequest.SizeGiB != 10 {
		t.Fatalf("expected size 10GiB, got %dGiB", fake.ensureRequest.SizeGiB)
	}
	if resp.GetVolume().GetVolumeContext()[morpheus.VolumeContextSizeGiB] != "10" {
		t.Fatalf("expected volume context size 10GiB, got %q", resp.GetVolume().GetVolumeContext()[morpheus.VolumeContextSizeGiB])
	}
	if resp.GetVolume().GetVolumeContext()[morpheus.VolumeContextVolumeID] != "test-volume-never-real" {
		t.Fatalf("expected volume context ID test-volume-never-real, got %q", resp.GetVolume().GetVolumeContext()[morpheus.VolumeContextVolumeID])
	}
}

func TestControllerRPCsReturnUnavailableWhenMorpheusClientUnconfigured(t *testing.T) {
	// config.Default has no Morpheus URL/token, so New cannot build a
	// Morpheus client. The controller guards must report Unavailable rather
	// than leaving a typed-nil *morpheus.Client in the interface field, which
	// would slip past the nil check and panic on the first API call.
	driver := New(config.Default(), log.Default())

	_, err := driver.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:               "pvc-123",
		CapacityRange:      &csi.CapacityRange{RequiredBytes: gibibyte},
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability()},
		Parameters:         map[string]string{morpheus.ParamServerID: "test-server-never-real"},
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable from CreateVolume, got %v", err)
	}

	_, err = driver.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{
		VolumeId: "test-server-never-real:test-volume-never-real",
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable from DeleteVolume, got %v", err)
	}
}

func TestCreateVolumeRejectsMissingServerID(t *testing.T) {
	fake := &fakeMorpheus{}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	_, err := driver.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:               "pvc-123",
		CapacityRange:      &csi.CapacityRange{RequiredBytes: gibibyte},
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability()},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if fake.ensureCalled {
		t.Fatal("EnsureVolume should not be called for invalid parameters")
	}
}

func TestDeleteVolumeParsesServerVolumeID(t *testing.T) {
	fake := &fakeMorpheus{}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	_, err := driver.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "test-server-never-real:test-volume-never-real"})
	if err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	if fake.deletedRef != (morpheus.VolumeRef{ServerID: "test-server-never-real", VolumeID: "test-volume-never-real"}) {
		t.Fatalf("unexpected deleted ref: %+v", fake.deletedRef)
	}
}

func TestControllerPublishReturnsDevicePathFromServerVolume(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:         "test-volume-never-real",
			Name:       "pvc-123",
			DevicePath: "/dev/disk/by-id/morpheus-test-volume",
		},
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	resp, err := driver.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "test-server-never-real:test-volume-never-real",
		NodeId:   "worker-1",
		VolumeContext: map[string]string{
			morpheus.VolumeContextServerID: "test-server-never-real",
		},
	})
	if err != nil {
		t.Fatalf("ControllerPublishVolume returned error: %v", err)
	}
	if got := resp.GetPublishContext()[morpheus.VolumeContextDevicePath]; got != "/dev/disk/by-id/morpheus-test-volume" {
		t.Fatalf("expected device path in publish context, got %q", got)
	}
}

func TestNodeResolverReadsServerIDLabel(t *testing.T) {
	resolver := &fakeNodeResolver{nodeServers: map[string]string{"worker-2": "896"}}

	serverID, err := resolver.ServerIDForNode(context.Background(), "worker-2")
	if err != nil {
		t.Fatalf("ServerIDForNode returned error: %v", err)
	}
	if serverID != "896" {
		t.Fatalf("expected server ID 896, got %q", serverID)
	}
}

func TestControllerPublishMovesVolumeToRequestedNodeServer(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:      "test-volume-never-real",
			Name:    "pvc-123",
			SizeGiB: 14,
		},
	}
	nodes := &fakeNodeResolver{nodeServers: map[string]string{"worker-2": "target-server-never-real"}}
	driver := NewWithDependenciesAndNodeResolver(config.Default(), log.Default(), fake, fake, &fakeMounter{}, nodes)

	resp, err := driver.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "source-server-never-real:test-volume-never-real",
		NodeId:   "worker-2",
		VolumeContext: map[string]string{
			morpheus.VolumeContextSizeGiB: "14",
		},
	})
	if err != nil {
		t.Fatalf("ControllerPublishVolume returned error: %v", err)
	}
	if fake.moveRequest.TargetServerID != "target-server-never-real" {
		t.Fatalf("expected move target server target-server-never-real, got %q", fake.moveRequest.TargetServerID)
	}
	if fake.moveRequest.VolumeID != "test-volume-never-real" {
		t.Fatalf("expected move volume test-volume-never-real, got %q", fake.moveRequest.VolumeID)
	}
	if got := resp.GetPublishContext()[morpheus.VolumeContextServerID]; got != "target-server-never-real" {
		t.Fatalf("expected publish context target server, got %q", got)
	}
	if got := resp.GetPublishContext()[morpheus.VolumeContextVolumeID]; got != "test-volume-never-real" {
		t.Fatalf("expected publish context volume ID, got %q", got)
	}
}

func TestControllerPublishFallsBackToMorpheusNodeNameLookup(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:      "test-volume-never-real",
			Name:    "pvc-123",
			SizeGiB: 14,
		},
		nodeServers: map[string]string{"worker-2": "target-server-never-real"},
	}
	driver := NewWithDependenciesAndNodeResolver(config.Default(), log.Default(), fake, fake, &fakeMounter{}, nil)

	_, err := driver.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "source-server-never-real:test-volume-never-real",
		NodeId:   "worker-2",
	})
	if err != nil {
		t.Fatalf("ControllerPublishVolume returned error: %v", err)
	}
	if fake.moveRequest.TargetServerID != "target-server-never-real" {
		t.Fatalf("expected Morpheus lookup target server, got %q", fake.moveRequest.TargetServerID)
	}
}

func TestControllerPublishPrefersNodeLabelOverMorpheusLookup(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:      "test-volume-never-real",
			Name:    "pvc-123",
			SizeGiB: 14,
		},
		nodeServers: map[string]string{"worker-2": "lookup-server-never-real"},
	}
	nodes := &fakeNodeResolver{nodeServers: map[string]string{"worker-2": "label-server-never-real"}}
	driver := NewWithDependenciesAndNodeResolver(config.Default(), log.Default(), fake, fake, &fakeMounter{}, nodes)

	_, err := driver.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "source-server-never-real:test-volume-never-real",
		NodeId:   "worker-2",
	})
	if err != nil {
		t.Fatalf("ControllerPublishVolume returned error: %v", err)
	}
	if fake.moveRequest.TargetServerID != "label-server-never-real" {
		t.Fatalf("expected label target server, got %q", fake.moveRequest.TargetServerID)
	}
}

func TestControllerPublishIsIdempotentOnRequestedNodeServer(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:      "test-volume-never-real",
			Name:    "pvc-123",
			SizeGiB: 14,
		},
	}
	nodes := &fakeNodeResolver{nodeServers: map[string]string{"worker-2": "source-server-never-real"}}
	driver := NewWithDependenciesAndNodeResolver(config.Default(), log.Default(), fake, fake, &fakeMounter{}, nodes)

	_, err := driver.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "source-server-never-real:test-volume-never-real",
		NodeId:   "worker-2",
	})
	if err != nil {
		t.Fatalf("ControllerPublishVolume returned error: %v", err)
	}
	if fake.moveRequest.TargetServerID != "source-server-never-real" {
		t.Fatalf("expected idempotent attach target source-server-never-real, got %q", fake.moveRequest.TargetServerID)
	}
}

func TestControllerUnpublishDetachesButDoesNotDelete(t *testing.T) {
	fake := &fakeMorpheus{}
	nodes := &fakeNodeResolver{nodeServers: map[string]string{"worker-2": "target-server-never-real"}}
	driver := NewWithDependenciesAndNodeResolver(config.Default(), log.Default(), fake, fake, &fakeMounter{}, nodes)

	_, err := driver.ControllerUnpublishVolume(context.Background(), &csi.ControllerUnpublishVolumeRequest{
		VolumeId: "source-server-never-real:test-volume-never-real",
		NodeId:   "worker-2",
	})
	if err != nil {
		t.Fatalf("ControllerUnpublishVolume returned error: %v", err)
	}
	if fake.detachedRef != (morpheus.VolumeRef{ServerID: "target-server-never-real", VolumeID: "test-volume-never-real"}) {
		t.Fatalf("unexpected detached ref: %+v", fake.detachedRef)
	}
	if fake.deletedRef != (morpheus.VolumeRef{}) {
		t.Fatalf("ControllerUnpublishVolume should not delete volume, got deleted ref %+v", fake.deletedRef)
	}
}

func TestControllerExpandVolumeUsesServerResize(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{ID: "test-volume-never-real", Name: "pvc-123", SizeGiB: 20},
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	resp, err := driver.ControllerExpandVolume(context.Background(), &csi.ControllerExpandVolumeRequest{
		VolumeId:         "test-server-never-real:test-volume-never-real",
		CapacityRange:    &csi.CapacityRange{RequiredBytes: 20 * gibibyte},
		VolumeCapability: mountCapability(),
	})
	if err != nil {
		t.Fatalf("ControllerExpandVolume returned error: %v", err)
	}
	if !resp.GetNodeExpansionRequired() {
		t.Fatal("expected node expansion to be required")
	}
	if fake.expandRequest.VolumeID != "test-volume-never-real" || fake.expandRequest.StorageClass[morpheus.ParamServerID] != "test-server-never-real" {
		t.Fatalf("unexpected expand request: %+v", fake.expandRequest)
	}
}

func TestNodeStageRequiresDevicePath(t *testing.T) {
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, &fakeMounter{discoverErr: errors.New("no candidate")})

	_, err := driver.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "test-server-never-real:test-volume-never-real",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
		VolumeCapability:  mountCapability(),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestNodeStageDiscoversDevicePathWhenPublishContextIsMissing(t *testing.T) {
	mounter := &fakeMounter{discoverPath: "/dev/disk/by-path/test-disk"}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "test-server-never-real:test-volume-never-real",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
		VolumeCapability:  mountCapability(),
		VolumeContext: map[string]string{
			morpheus.VolumeContextSizeGiB: "14",
		},
	})
	if err != nil {
		t.Fatalf("NodeStageVolume returned error: %v", err)
	}
	if !mounter.rescanCalled {
		t.Fatal("expected node rescan before staging")
	}
	if mounter.discoverSizeGiB != 14 {
		t.Fatalf("expected discovery size 14GiB, got %dGiB", mounter.discoverSizeGiB)
	}
	if mounter.stageDevicePath != "/dev/disk/by-path/test-disk" {
		t.Fatalf("expected discovered device path to be staged, got %q", mounter.stageDevicePath)
	}
}

func TestNodeStageUsesMorpheusProvidedDevicePath(t *testing.T) {
	mounter := &fakeMounter{discoverPath: "/dev/disk/by-path/should-not-be-used"}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "test-server-never-real:test-volume-never-real",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
		VolumeCapability:  mountCapability(),
		PublishContext: map[string]string{
			morpheus.VolumeContextDevicePath: "/dev/sdb",
		},
	})
	if err != nil {
		t.Fatalf("NodeStageVolume returned error: %v", err)
	}
	if !mounter.rescanCalled {
		t.Fatal("expected node rescan before staging")
	}
	if mounter.discoverCalled {
		t.Fatal("expected discovery to be skipped when Morpheus provided a device path")
	}
	if mounter.stageDevicePath != "/dev/sdb" {
		t.Fatalf("expected Morpheus device path to be staged, got %q", mounter.stageDevicePath)
	}
}

func TestNodeStageRecordsDeviceMetadata(t *testing.T) {
	mounter := &fakeMounter{}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "test-server-never-real:test-volume-never-real",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
		VolumeCapability:  mountCapability(),
		PublishContext: map[string]string{
			morpheus.VolumeContextDevicePath: "/dev/sdb",
		},
	})
	if err != nil {
		t.Fatalf("NodeStageVolume returned error: %v", err)
	}
	if mounter.recordedVolumeID != "test-server-never-real:test-volume-never-real" {
		t.Fatalf("expected recorded volume ID, got %q", mounter.recordedVolumeID)
	}
	if mounter.recordedDevicePath != "/dev/sdb" {
		t.Fatalf("expected recorded device path /dev/sdb, got %q", mounter.recordedDevicePath)
	}
	if mounter.recordedStagingPath != "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount" {
		t.Fatalf("expected recorded staging path, got %q", mounter.recordedStagingPath)
	}
}

func TestCandidateBlockDevicesDiscoversSingleSafeSizeMatch(t *testing.T) {
	withFakeDeviceTree(t, []fakeBlockDevice{
		{name: "sdb", sizeGiB: 14},
		{name: "sdc", sizeGiB: 10},
	}, "", func(devRoot string) {
		candidates, err := candidateBlockDevices(context.Background(), 14)
		if err != nil {
			t.Fatalf("candidateBlockDevices returned error: %v", err)
		}
		expected := filepath.Join(devRoot, "sdb")
		if len(candidates) != 1 || candidates[0] != expected {
			t.Fatalf("expected one candidate %q, got %#v", expected, candidates)
		}
	})
}

func TestCandidateBlockDevicesRejectsAmbiguousSizeMatches(t *testing.T) {
	withFakeDeviceTree(t, []fakeBlockDevice{
		{name: "sdb", sizeGiB: 14},
		{name: "sdc", sizeGiB: 14},
	}, "", func(string) {
		_, err := realMounter{}.DiscoverDevicePath(context.Background(), 14)
		if err == nil {
			t.Fatal("expected ambiguous candidates to fail")
		}
	})
}

func TestCandidateBlockDevicesIgnoresUnsafeDevices(t *testing.T) {
	withFakeDeviceTree(t, []fakeBlockDevice{
		{name: "sdb", sizeGiB: 14, partitioned: true},
		{name: "sdc", sizeGiB: 14, formatted: true},
		{name: "sdd", sizeGiB: 14},
		{name: "sr0", sizeGiB: 14},
		{name: "nbd0", sizeGiB: 14},
	}, "", func(devRoot string) {
		candidates, err := candidateBlockDevices(context.Background(), 14)
		if err != nil {
			t.Fatalf("candidateBlockDevices returned error: %v", err)
		}
		expected := filepath.Join(devRoot, "sdd")
		if len(candidates) != 1 || candidates[0] != expected {
			t.Fatalf("expected only safe candidate %q, got %#v", expected, candidates)
		}
	})
}

func TestNodeUnstageUnmountsBeforeRemovingDevice(t *testing.T) {
	mounter := &fakeMounter{mountedSource: "/dev/sdb"}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "test-server-never-real:test-volume-never-real",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
	})
	if err != nil {
		t.Fatalf("NodeUnstageVolume returned error: %v", err)
	}
	if got := mounter.operations; len(got) != 4 || got[0] != "source" || got[1] != "unmount" || got[2] != "remove" || got[3] != "forget" {
		t.Fatalf("expected source, unmount, remove, forget operations, got %#v", got)
	}
	if mounter.removedDevicePath != "/dev/sdb" {
		t.Fatalf("expected /dev/sdb to be removed, got %q", mounter.removedDevicePath)
	}
}

func TestNodeUnpublishOnlyUnmountsPodTarget(t *testing.T) {
	mounter := &fakeMounter{}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{
		VolumeId:   "test-server-never-real:test-volume-never-real",
		TargetPath: "/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pv/mount",
	})
	if err != nil {
		t.Fatalf("NodeUnpublishVolume returned error: %v", err)
	}
	if got := mounter.operations; len(got) != 1 || got[0] != "unmount" {
		t.Fatalf("expected only pod target unmount, got %#v", got)
	}
}

func TestNodeUnstageFallsBackToPersistedDeviceMetadata(t *testing.T) {
	mounter := &fakeMounter{stagedDevicePath: "/dev/sdb"}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "test-server-never-real:test-volume-never-real",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
	})
	if err != nil {
		t.Fatalf("NodeUnstageVolume returned error: %v", err)
	}
	if got := mounter.operations; len(got) != 5 || got[0] != "source" || got[1] != "metadata" || got[2] != "unmount" || got[3] != "remove" || got[4] != "forget" {
		t.Fatalf("expected source, metadata, unmount, remove, forget operations, got %#v", got)
	}
	if mounter.removedDevicePath != "/dev/sdb" {
		t.Fatalf("expected persisted /dev/sdb to be removed, got %q", mounter.removedDevicePath)
	}
}

func TestNodeUnstageKeepsMetadataWhenDeviceRemovalFails(t *testing.T) {
	mounter := &fakeMounter{
		stagedDevicePath: "/dev/sdb",
		removeErr:        errors.New("remove failed"),
	}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId:          "test-server-never-real:test-volume-never-real",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
	})
	if err == nil {
		t.Fatal("expected NodeUnstageVolume to fail when device removal fails")
	}
	if mounter.forgetCalled {
		t.Fatal("expected staged device metadata to remain when device removal fails")
	}
}

func TestNodeExpandVolumeCallsMounter(t *testing.T) {
	mounter := &fakeMounter{}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
		VolumeId:         "test-server-never-real:test-volume-never-real",
		VolumePath:       "/var/lib/kubelet/pods/pod/volumes/kubernetes.io~csi/pv/mount",
		CapacityRange:    &csi.CapacityRange{RequiredBytes: 20 * gibibyte},
		VolumeCapability: mountCapability(),
	})
	if err != nil {
		t.Fatalf("NodeExpandVolume returned error: %v", err)
	}
	if !mounter.expandCalled || mounter.expandPath == "" {
		t.Fatalf("expected ExpandFilesystem to be called, got %+v", mounter)
	}
}

func mountCapability() *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}
}

type fakeMorpheus struct {
	volume        *morpheus.StorageVolume
	ensureRequest morpheus.ResizeVolumeRequest
	expandRequest morpheus.ResizeVolumeRequest
	moveRequest   morpheus.MoveVolumeRequest
	ensureCalled  bool
	deletedRef    morpheus.VolumeRef
	detachedRef   morpheus.VolumeRef
	nodeServers   map[string]string
	err           error
}

func (f *fakeMorpheus) EnsureVolume(_ context.Context, req morpheus.ResizeVolumeRequest) (*morpheus.StorageVolume, error) {
	f.ensureCalled = true
	f.ensureRequest = req
	if f.err != nil {
		return nil, f.err
	}
	if f.volume == nil {
		return nil, errors.New("missing fake volume")
	}
	return f.volume, nil
}

func (f *fakeMorpheus) DeleteVolume(_ context.Context, ref morpheus.VolumeRef) error {
	f.deletedRef = ref
	return f.err
}

func (f *fakeMorpheus) ExpandVolume(_ context.Context, req morpheus.ResizeVolumeRequest) (*morpheus.StorageVolume, error) {
	f.expandRequest = req
	if f.err != nil {
		return nil, f.err
	}
	if f.volume == nil {
		return nil, errors.New("missing fake volume")
	}
	return f.volume, nil
}

func (f *fakeMorpheus) GetVolume(context.Context, morpheus.VolumeRef) (*morpheus.StorageVolume, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.volume == nil {
		return nil, errors.New("missing fake volume")
	}
	return f.volume, nil
}

func (f *fakeMorpheus) FindVolume(_ context.Context, volumeID string, serverIDs []string) (morpheus.VolumeRef, *morpheus.StorageVolume, error) {
	if f.err != nil {
		return morpheus.VolumeRef{}, nil, f.err
	}
	if f.volume == nil {
		return morpheus.VolumeRef{}, nil, errors.New("missing fake volume")
	}
	serverID := "test-server-never-real"
	if len(serverIDs) > 0 {
		serverID = serverIDs[0]
	}
	return morpheus.VolumeRef{ServerID: serverID, VolumeID: volumeID}, f.volume, nil
}

func (f *fakeMorpheus) MoveVolume(_ context.Context, req morpheus.MoveVolumeRequest) (*morpheus.StorageVolume, error) {
	f.moveRequest = req
	if f.err != nil {
		return nil, f.err
	}
	if f.volume == nil {
		return nil, errors.New("missing fake volume")
	}
	return f.volume, nil
}

func (f *fakeMorpheus) DetachVolume(_ context.Context, ref morpheus.VolumeRef) error {
	f.detachedRef = ref
	return f.err
}

func (f *fakeMorpheus) ValidateStorageClass(_ context.Context, parameters map[string]string) error {
	if parameters[morpheus.ParamServerID] == "" {
		return errors.New("server id is required")
	}
	return nil
}

func (f *fakeMorpheus) ResolveServerIDByNodeName(_ context.Context, nodeName string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	serverID := f.nodeServers[nodeName]
	if serverID == "" {
		return "", errors.New("missing fake node lookup")
	}
	return serverID, nil
}

type fakeMounter struct {
	expandCalled        bool
	expandPath          string
	rescanCalled        bool
	discoverCalled      bool
	discoverPath        string
	discoverErr         error
	discoverSizeGiB     int64
	stageDevicePath     string
	recordedVolumeID    string
	recordedDevicePath  string
	recordedStagingPath string
	stagedDevicePath    string
	mountedSource       string
	removedDevicePath   string
	removeErr           error
	forgetCalled        bool
	operations          []string
}

func (f *fakeMounter) Stage(_ context.Context, devicePath string, _ string, _ string, _ bool) error {
	f.stageDevicePath = devicePath
	return nil
}

func (f *fakeMounter) BindMount(context.Context, string, string, bool) error {
	return nil
}

func (f *fakeMounter) MountedSource(context.Context, string) (string, error) {
	f.operations = append(f.operations, "source")
	if f.mountedSource == "" {
		return "", errors.New("not mounted")
	}
	return f.mountedSource, nil
}

func (f *fakeMounter) Unmount(context.Context, string) error {
	f.operations = append(f.operations, "unmount")
	return nil
}

func (f *fakeMounter) ExpandFilesystem(_ context.Context, volumePath string, _ string) error {
	f.expandCalled = true
	f.expandPath = volumePath
	return nil
}

func (f *fakeMounter) RescanDevices(context.Context) error {
	f.rescanCalled = true
	return nil
}

func (f *fakeMounter) DiscoverDevicePath(_ context.Context, sizeGiB int64) (string, error) {
	f.discoverCalled = true
	f.discoverSizeGiB = sizeGiB
	if f.discoverErr != nil {
		return "", f.discoverErr
	}
	if f.discoverPath == "" {
		return "", errors.New("missing fake discovered device path")
	}
	return f.discoverPath, nil
}

func (f *fakeMounter) RemoveDevice(_ context.Context, devicePath string) error {
	f.operations = append(f.operations, "remove")
	f.removedDevicePath = devicePath
	if f.removeErr != nil {
		return f.removeErr
	}
	return nil
}

func (f *fakeMounter) RecordStagedDevice(_ context.Context, volumeID string, devicePath string, stagingPath string) error {
	f.recordedVolumeID = volumeID
	f.recordedDevicePath = devicePath
	f.recordedStagingPath = stagingPath
	return nil
}

func (f *fakeMounter) StagedDevicePath(context.Context, string) (string, error) {
	f.operations = append(f.operations, "metadata")
	if f.stagedDevicePath == "" {
		return "", errors.New("missing fake staged device path")
	}
	return f.stagedDevicePath, nil
}

func (f *fakeMounter) ForgetStagedDevice(context.Context, string) error {
	f.operations = append(f.operations, "forget")
	f.forgetCalled = true
	return nil
}

type fakeNodeResolver struct {
	nodeServers map[string]string
	err         error
}

func (f *fakeNodeResolver) ServerIDForNode(_ context.Context, nodeName string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	serverID := f.nodeServers[nodeName]
	if serverID == "" {
		return "", errors.New("missing fake node server")
	}
	return serverID, nil
}

func (f *fakeNodeResolver) ServerIDs(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	var ids []string
	for _, serverID := range f.nodeServers {
		ids = append(ids, serverID)
	}
	return ids, nil
}

type fakeBlockDevice struct {
	name        string
	sizeGiB     int64
	partitioned bool
	formatted   bool
}

func withFakeDeviceTree(t *testing.T, devices []fakeBlockDevice, mountInfo string, test func(devRoot string)) {
	t.Helper()
	root := t.TempDir()
	previousSysBlockRoot := sysBlockRoot
	previousDevRoot := devRoot
	previousDevDiskByPathRoot := devDiskByPathRoot
	previousMountInfoPath := mountInfoPath
	previousHasFilesystemFunc := hasFilesystemFunc
	defer func() {
		sysBlockRoot = previousSysBlockRoot
		devRoot = previousDevRoot
		devDiskByPathRoot = previousDevDiskByPathRoot
		mountInfoPath = previousMountInfoPath
		hasFilesystemFunc = previousHasFilesystemFunc
	}()

	sysBlockRoot = filepath.Join(root, "sys", "block")
	devRoot = filepath.Join(root, "dev")
	devDiskByPathRoot = filepath.Join(devRoot, "disk", "by-path")
	mountInfoPath = filepath.Join(root, "mountinfo")
	if err := os.MkdirAll(sysBlockRoot, 0750); err != nil {
		t.Fatalf("create fake sys block root: %v", err)
	}
	if err := os.MkdirAll(devDiskByPathRoot, 0750); err != nil {
		t.Fatalf("create fake dev root: %v", err)
	}
	if err := os.WriteFile(mountInfoPath, []byte(mountInfo), 0640); err != nil {
		t.Fatalf("write fake mountinfo: %v", err)
	}

	formatted := map[string]bool{}
	for _, device := range devices {
		deviceDir := filepath.Join(sysBlockRoot, device.name)
		if err := os.MkdirAll(filepath.Join(deviceDir, "device"), 0750); err != nil {
			t.Fatalf("create fake device: %v", err)
		}
		sectors := device.sizeGiB * gibibyte / 512
		if err := os.WriteFile(filepath.Join(deviceDir, "size"), []byte(strconv.FormatInt(sectors, 10)), 0640); err != nil {
			t.Fatalf("write fake device size: %v", err)
		}
		if err := os.WriteFile(filepath.Join(deviceDir, "device", "delete"), nil, 0200); err != nil {
			t.Fatalf("write fake device delete file: %v", err)
		}
		if err := os.WriteFile(filepath.Join(devRoot, device.name), nil, 0640); err != nil {
			t.Fatalf("write fake dev node: %v", err)
		}
		if device.partitioned {
			partitionDir := filepath.Join(deviceDir, device.name+"1")
			if err := os.MkdirAll(partitionDir, 0750); err != nil {
				t.Fatalf("create fake partition: %v", err)
			}
			if err := os.WriteFile(filepath.Join(partitionDir, "partition"), nil, 0640); err != nil {
				t.Fatalf("write fake partition marker: %v", err)
			}
		}
		formatted[filepath.Join(devRoot, device.name)] = device.formatted
	}
	hasFilesystemFunc = func(_ context.Context, devicePath string) (bool, error) {
		return formatted[devicePath], nil
	}

	test(devRoot)
}
