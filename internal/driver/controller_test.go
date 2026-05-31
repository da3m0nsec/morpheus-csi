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

func TestCreateVolumeCreatesMorpheusVolume(t *testing.T) {
	fake := &fakeMorpheus{
		volume: &morpheus.StorageVolume{
			ID:       "42",
			Name:     "pvc-123",
			SizeGiB:  10,
			ServerID: "12",
			TypeID:   "4",
		},
		serverID: "12",
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, fake, fakeMounter{})

	resp, err := driver.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:          "pvc-123",
		CapacityRange: &csi.CapacityRange{RequiredBytes: 10 * gibibyte},
		Parameters: map[string]string{
			morpheus.ParamStorageServerID:     "12",
			morpheus.ParamStorageVolumeTypeID: "4",
			"csi.storage.k8s.io/fstype":       "ext4",
		},
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability()},
	})
	if err != nil {
		t.Fatalf("CreateVolume returned error: %v", err)
	}
	if resp.GetVolume().GetVolumeId() != "42" {
		t.Fatalf("expected volume id 42, got %q", resp.GetVolume().GetVolumeId())
	}
	if fake.createRequest.SizeGiB != 10 {
		t.Fatalf("expected size 10GiB, got %dGiB", fake.createRequest.SizeGiB)
	}
}

func TestCreateVolumeRejectsMissingStorageClassParameters(t *testing.T) {
	fake := &fakeMorpheus{}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, fake, fakeMounter{})

	_, err := driver.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name:               "pvc-123",
		CapacityRange:      &csi.CapacityRange{RequiredBytes: gibibyte},
		VolumeCapabilities: []*csi.VolumeCapability{mountCapability()},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if fake.createCalled {
		t.Fatal("CreateStorageVolume should not be called for invalid parameters")
	}
}

func TestDeleteVolumeToleratesClientSuccess(t *testing.T) {
	fake := &fakeMorpheus{}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, fake, fakeMounter{})

	_, err := driver.DeleteVolume(context.Background(), &csi.DeleteVolumeRequest{VolumeId: "42"})
	if err != nil {
		t.Fatalf("DeleteVolume returned error: %v", err)
	}
	if fake.deletedVolumeID != "42" {
		t.Fatalf("expected deleted volume id 42, got %q", fake.deletedVolumeID)
	}
}

func TestControllerPublishReturnsDevicePathWhenAttachProvidesIt(t *testing.T) {
	fake := &fakeMorpheus{
		serverID: "server-7",
		attachedVolume: &morpheus.StorageVolume{
			ID:         "42",
			ServerID:   "server-7",
			DevicePath: "/dev/disk/by-id/morpheus-42",
		},
	}
	driver := NewWithDependencies(config.Default(), log.Default(), fake, fake, fake, fakeMounter{})

	resp, err := driver.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "42",
		NodeId:   "worker-1",
	})
	if err != nil {
		t.Fatalf("ControllerPublishVolume returned error: %v", err)
	}
	if got := resp.GetPublishContext()["morpheus.devicePath"]; got != "/dev/disk/by-id/morpheus-42" {
		t.Fatalf("expected device path in publish context, got %q", got)
	}
}

func TestNodeStageRequiresDevicePath(t *testing.T) {
	driver := NewWithDependencies(config.Config{NodeID: "worker-1"}, log.Default(), nil, nil, nil, fakeMounter{})

	_, err := driver.NodeStageVolume(context.Background(), &csi.NodeStageVolumeRequest{
		VolumeId:          "42",
		StagingTargetPath: "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/42/globalmount",
		VolumeCapability:  mountCapability(),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
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
	volume          *morpheus.StorageVolume
	attachedVolume  *morpheus.StorageVolume
	createRequest   morpheus.CreateStorageVolumeRequest
	createCalled    bool
	deletedVolumeID string
	serverID        string
	err             error
}

func (f *fakeMorpheus) CreateStorageVolume(_ context.Context, req morpheus.CreateStorageVolumeRequest) (*morpheus.StorageVolume, error) {
	f.createCalled = true
	f.createRequest = req
	if f.err != nil {
		return nil, f.err
	}
	if f.volume == nil {
		return nil, errors.New("missing fake volume")
	}
	return f.volume, nil
}

func (f *fakeMorpheus) DeleteStorageVolume(_ context.Context, volumeID string) error {
	f.deletedVolumeID = volumeID
	return f.err
}

func (f *fakeMorpheus) AttachStorageVolume(_ context.Context, _ morpheus.AttachStorageVolumeRequest) (*morpheus.StorageVolume, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.attachedVolume, nil
}

func (f *fakeMorpheus) DetachStorageVolume(context.Context, string, string) error {
	return f.err
}

func (f *fakeMorpheus) ValidateStorageClass(_ context.Context, parameters map[string]string) error {
	if parameters[morpheus.ParamStorageServerID] == "" {
		return errors.New("storage server id is required")
	}
	if parameters[morpheus.ParamStorageVolumeTypeID] == "" {
		return errors.New("storage volume type id is required")
	}
	return nil
}

func (f *fakeMorpheus) ResolveServerIDForNode(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.serverID == "" {
		return "", errors.New("missing fake server id")
	}
	return f.serverID, nil
}

type fakeMounter struct {
	stageCalled bool
}

func (f fakeMounter) Stage(context.Context, string, string, string, bool) error {
	return nil
}

func (f fakeMounter) BindMount(context.Context, string, string, bool) error {
	return nil
}

func (f fakeMounter) Unmount(context.Context, string) error {
	return nil
}
