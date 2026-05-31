package driver

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/da3m0nsec/morpheus-csi/internal/morpheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *Driver) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			controllerCapability(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME),
			controllerCapability(csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME),
		},
	}, nil
}

func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if d.volumes == nil || d.discovery == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}
	if err := validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := d.discovery.ValidateStorageClass(ctx, req.GetParameters()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	sizeGiB, err := requestedSizeGiB(req.GetCapacityRange())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	volume, err := d.volumes.CreateStorageVolume(ctx, morpheus.CreateStorageVolumeRequest{
		Name:         name,
		SizeGiB:      sizeGiB,
		StorageClass: req.GetParameters(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create Morpheus storage volume: %v", err)
	}

	context := map[string]string{
		morpheus.ParamStorageServerID:     volume.ServerID,
		morpheus.ParamStorageVolumeTypeID: volume.TypeID,
		"morpheus.volumeName":             volume.Name,
	}
	if volume.DevicePath != "" {
		context["morpheus.devicePath"] = volume.DevicePath
	}
	if fsType := req.GetParameters()["csi.storage.k8s.io/fstype"]; fsType != "" {
		context["csi.storage.k8s.io/fstype"] = fsType
	}
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volume.ID,
			CapacityBytes: sizeGiB * gibibyte,
			VolumeContext: context,
		},
	}, nil
}

func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if d.volumes == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	volumeID := strings.TrimSpace(req.GetVolumeId())
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if err := d.volumes.DeleteStorageVolume(ctx, volumeID); err != nil {
		return nil, status.Errorf(codes.Internal, "delete Morpheus storage volume: %v", err)
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if d.attachments == nil || d.discovery == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	volumeID := strings.TrimSpace(req.GetVolumeId())
	nodeID := strings.TrimSpace(req.GetNodeId())
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node id is required")
	}

	serverID, err := d.discovery.ResolveServerIDForNode(ctx, nodeID)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "resolve Morpheus server for node %q: %v", nodeID, err)
	}
	volume, err := d.attachments.AttachStorageVolume(ctx, morpheus.AttachStorageVolumeRequest{
		ServerID: serverID,
		VolumeID: volumeID,
		NodeID:   nodeID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "attach Morpheus storage volume: %v", err)
	}

	publishContext := map[string]string{
		morpheus.ParamStorageServerID: serverID,
	}
	if volume != nil && volume.DevicePath != "" {
		publishContext["morpheus.devicePath"] = volume.DevicePath
	}
	return &csi.ControllerPublishVolumeResponse{PublishContext: publishContext}, nil
}

func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if d.attachments == nil || d.discovery == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	volumeID := strings.TrimSpace(req.GetVolumeId())
	nodeID := strings.TrimSpace(req.GetNodeId())
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node id is required")
	}

	serverID, err := d.discovery.ResolveServerIDForNode(ctx, nodeID)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "resolve Morpheus server for node %q: %v", nodeID, err)
	}
	if err := d.attachments.DetachStorageVolume(ctx, serverID, volumeID); err != nil {
		return nil, status.Errorf(codes.Internal, "detach Morpheus storage volume: %v", err)
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func controllerCapability(capability csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: capability,
			},
		},
	}
}

const gibibyte int64 = 1024 * 1024 * 1024

func requestedSizeGiB(capacity *csi.CapacityRange) (int64, error) {
	if capacity == nil {
		return 0, errors.New("capacity range is required")
	}
	required := capacity.GetRequiredBytes()
	limit := capacity.GetLimitBytes()
	if required <= 0 {
		return 0, errors.New("required capacity must be greater than zero")
	}
	if limit > 0 && required > limit {
		return 0, errors.New("required capacity exceeds limit capacity")
	}
	return int64(math.Ceil(float64(required) / float64(gibibyte))), nil
}

func validateVolumeCapabilities(capabilities []*csi.VolumeCapability) error {
	if len(capabilities) == 0 {
		return errors.New("at least one volume capability is required")
	}
	for _, capability := range capabilities {
		if capability == nil {
			return errors.New("volume capability cannot be nil")
		}
		if capability.GetBlock() != nil {
			return errors.New("raw block volumes are not supported")
		}
		if capability.GetMount() == nil {
			return errors.New("mount volume capability is required")
		}
		mode := capability.GetAccessMode().GetMode()
		switch mode {
		case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
			csi.VolumeCapability_AccessMode_UNKNOWN:
		default:
			return errors.New("only single-node writer access mode is supported")
		}
	}
	return nil
}
