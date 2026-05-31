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

func TestCreateVolumeEnsuresInstanceVolume(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:      "42",
			Name:    "pvc-123",
			SizeGiB: 10,
		},
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	resp, err := driver.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:          "pvc-123",
		CapacityRange: &csi.CapacityRange{RequiredBytes: 10 * gibibyte},
		Parameters: map[string]string{
			morpheus.ParamInstanceID:    "instance-7",
			"csi.storage.k8s.io/fstype": "ext4",
		},
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability()},
	})
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if resp.GetVolume().GetVolumeId() != "instance-7:42" {
		t.Fatalf("expected encoded volume id instance-7:42, got %q", resp.GetVolume().GetVolumeId())
	}
	if fake.ensureRequest.SizeGiB != 10 {
		t.Fatalf("expected size 10GiB, got %dGiB", fake.ensureRequest.SizeGiB)
	}
}

func TestCreateVolumeRejectsMissingInstanceID(t *testing.T) {
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

func TestDeleteVolumeParsesInstanceVolumeID(t *testing.T) {
	fake := &fakeMorpheus{}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	_, err := driver.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "instance-7:42"})
	if err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	if fake.deletedRef != (morpheus.VolumeRef{InstanceID: "instance-7", VolumeID: "42"}) {
		t.Fatalf("unexpected deleted ref: %+v", fake.deletedRef)
	}
}

func TestControllerPublishReturnsDevicePathFromInstanceVolume(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:         "42",
			Name:       "pvc-123",
			DevicePath: "/dev/disk/by-id/morpheus-42",
		},
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	resp, err := driver.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "instance-7:42",
		NodeId:   "worker-1",
		VolumeContext: map[string]string{
			morpheus.VolumeContextInstanceID: "instance-7",
		},
	})
	if err != nil {
		t.Fatalf("ControllerPublishVolume returned error: %v", err)
	}
	if got := resp.GetPublishContext()[morpheus.VolumeContextDevicePath]; got != "/dev/disk/by-id/morpheus-42" {
		t.Fatalf("expected device path in publish context, got %q", got)
	}
}

func TestControllerExpandVolumeUsesInstanceResize(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{ID: "42", Name: "pvc-123", SizeGiB: 20},
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, &fakeMounter{})

	resp, err := driver.ControllerExpandVolume(context.Background(), &csi.ControllerExpandVolumeRequest{
		VolumeId:         "instance-7:42",
		CapacityRange:    &csi.CapacityRange{RequiredBytes: 20 * gibibyte},
		VolumeCapability: mountCapability(),
	})
	if err != nil {
		t.Fatalf("ControllerExpandVolume returned error: %v", err)
	}
	if !resp.GetNodeExpansionRequired() {
		t.Fatal("expected node expansion to be required")
	}
	if fake.expandRequest.VolumeID != "42" || fake.expandRequest.StorageClass[morpheus.ParamInstanceID] != "instance-7" {
		t.Fatalf("unexpected expand request: %+v", fake.expandRequest)
	}
}

func TestNodeStageRequiresDevicePath(t *testing.T) {
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, &fakeMounter{})

	_, err := driver.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "instance-7:42",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
		VolumeCapability:  mountCapability(),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestNodeExpandVolumeCallsMounter(t *testing.T) {
	mounter := &fakeMounter{}
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, mounter)

	_, err := driver.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
		VolumeId:         "instance-7:42",
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
	if parameters[morpheus.ParamInstanceID] == "" {
		return errors.New("instance id is required")
	}
	return nil
}

type fakeMounter struct {
	expandCalled bool
	expandPath   string
}

func (f *fakeMounter) Stage(context.Context, string, string, string, bool) error {
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
