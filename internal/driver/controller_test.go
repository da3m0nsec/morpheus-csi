package driver

import (
	"context"
	"errors"
	"log"
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
	ensureCalled  bool
	deletedRef    morpheus.VolumeRef
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

func (f *fakeMorpheus) ValidateStorageClass(_ context.Context, parameters map[string]string) error {
	if parameters[morpheus.ParamServerID] == "" {
		return errors.New("server id is required")
	}
	return nil
}

type fakeMounter struct {
	expandCalled    bool
	expandPath      string
	rescanCalled    bool
	discoverPath    string
	discoverErr     error
	discoverSizeGiB int64
	stageDevicePath string
}

func (f *fakeMounter) Stage(_ context.Context, devicePath string, _ string, _ string, _ bool) error {
	f.stageDevicePath = devicePath
	return nil
}

func (f *fakeMounter) BindMount(context.Context, string, string, bool) error {
	return nil
}

func (f *fakeMounter) Unmount(context.Context, string) error {
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
	f.discoverSizeGiB = sizeGiB
	if f.discoverErr != nil {
		return "", f.discoverErr
	}
	if f.discoverPath == "" {
		return "", errors.New("missing fake discovered device path")
	}
	return f.discoverPath, nil
}
