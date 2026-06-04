package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
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
			controllerCapability(csi.ControllerServiceCapability_RPC_EXPAND_VOLUME),
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
	volume, err := d.volumes.EnsureVolume(ctx, morpheus.ResizeVolumeRequest{
		Name:         name,
		SizeGiB:      sizeGiB,
		StorageClass: req.GetParameters(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create Morpheus storage volume: %v", err)
	}

	serverID := morpheus.StorageClassServerID(req.GetParameters())
	context := map[string]string{
		morpheus.VolumeContextServerID:   serverID,
		morpheus.VolumeContextVolumeID:   volume.ID,
		morpheus.VolumeContextVolumeName: volume.Name,
		morpheus.VolumeContextSizeGiB:    strconv.FormatInt(sizeGiB, 10),
	}
	if volume.DevicePath != "" {
		context[morpheus.VolumeContextDevicePath] = volume.DevicePath
	}
	if fsType := req.GetParameters()["csi.storage.k8s.io/fstype"]; fsType != "" {
		context["csi.storage.k8s.io/fstype"] = fsType
	}
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      morpheus.EncodeVolumeID(serverID, volume.ID),
			CapacityBytes: sizeGiB * gibibyte,
			VolumeContext: context,
		},
	}, nil
}

func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if d.volumes == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	ref, err := morpheus.DecodeVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	currentRef := ref
	if candidates := d.candidateServerIDs(ctx, ref.ServerID); len(candidates) > 0 {
		foundRef, _, err := d.volumes.FindVolume(ctx, ref.VolumeID, candidates)
		if err == nil {
			currentRef = foundRef
		}
	}
	if err := d.volumes.DeleteVolume(ctx, currentRef); err != nil {
		return nil, status.Errorf(codes.Internal, "delete Morpheus server volume: %v", err)
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if d.volumes == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	ref, err := morpheus.DecodeVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "node id is required")
	}
	targetServerID, err := d.serverIDForNode(ctx, req.GetNodeId(), ref.ServerID)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	candidates := d.candidateServerIDs(ctx, ref.ServerID, targetServerID)
	volume, err := d.volumes.MoveVolume(ctx, morpheus.MoveVolumeRequest{
		VolumeID:           ref.VolumeID,
		SourceServerID:     ref.ServerID,
		TargetServerID:     targetServerID,
		CandidateServerIDs: candidates,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "attach Morpheus server volume to node %q: %v", req.GetNodeId(), err)
	}

	publishContext := map[string]string{
		morpheus.VolumeContextServerID:   targetServerID,
		morpheus.VolumeContextVolumeID:   ref.VolumeID,
		morpheus.VolumeContextVolumeName: volume.Name,
	}
	if sizeGiB := strings.TrimSpace(req.GetVolumeContext()[morpheus.VolumeContextSizeGiB]); sizeGiB != "" {
		publishContext[morpheus.VolumeContextSizeGiB] = sizeGiB
	}
	if volume != nil && volume.DevicePath != "" {
		publishContext[morpheus.VolumeContextDevicePath] = volume.DevicePath
	}
	return &csi.ControllerPublishVolumeResponse{PublishContext: publishContext}, nil
}

func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if d.volumes == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	ref, err := morpheus.DecodeVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "node id is required")
	}
	serverID, err := d.serverIDForNode(ctx, req.GetNodeId(), ref.ServerID)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if err := d.volumes.DetachVolume(ctx, morpheus.VolumeRef{ServerID: serverID, VolumeID: ref.VolumeID}); err != nil {
		return nil, status.Errorf(codes.Internal, "detach Morpheus server volume from node %q: %v", req.GetNodeId(), err)
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (d *Driver) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if d.volumes == nil {
		return nil, status.Error(codes.Unavailable, "Morpheus controller client is not configured")
	}
	ref, err := morpheus.DecodeVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.GetVolumeCapability() != nil {
		if err := validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	sizeGiB, err := requestedSizeGiB(req.GetCapacityRange())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	volume, err := d.volumes.ExpandVolume(ctx, morpheus.ResizeVolumeRequest{
		SizeGiB:  sizeGiB,
		VolumeID: ref.VolumeID,
		StorageClass: map[string]string{
			morpheus.ParamServerID: ref.ServerID,
		},
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "expand Morpheus server volume: %v", err)
	}
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         volume.SizeGiB * gibibyte,
		NodeExpansionRequired: true,
	}, nil
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

func (d *Driver) serverIDForNode(ctx context.Context, nodeID string, fallback string) (string, error) {
	var resolverErr error
	if d.nodes != nil {
		serverID, err := d.nodes.ServerIDForNode(ctx, nodeID)
		if err == nil {
			return serverID, nil
		}
		resolverErr = err
	}
	if lookup := d.morpheusNodeLookup(); lookup != nil {
		serverID, err := lookup.ResolveServerIDByNodeName(ctx, nodeID)
		if err == nil {
			return serverID, nil
		}
		if resolverErr != nil {
			return "", fmt.Errorf("%v; Morpheus node-name lookup also failed: %w", resolverErr, err)
		}
		resolverErr = err
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback), nil
	}
	if resolverErr != nil {
		return "", resolverErr
	}
	return "", errors.New("Kubernetes node server resolver and Morpheus node lookup are not configured")
}

func (d *Driver) morpheusNodeLookup() morpheus.NodeServerLookupClient {
	if lookup, ok := d.discovery.(morpheus.NodeServerLookupClient); ok {
		return lookup
	}
	if lookup, ok := d.volumes.(morpheus.NodeServerLookupClient); ok {
		return lookup
	}
	return nil
}

func (d *Driver) candidateServerIDs(ctx context.Context, extra ...string) []string {
	var candidates []string
	if d.nodes != nil {
		if serverIDs, err := d.nodes.ServerIDs(ctx); err == nil {
			candidates = append(candidates, serverIDs...)
		}
	}
	candidates = append(candidates, extra...)
	return uniqueStrings(candidates)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
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
