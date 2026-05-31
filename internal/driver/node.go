package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (d *Driver) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: d.cfg.NodeID,
	}, nil
}

func (d *Driver) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			nodeCapability(csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME),
		},
	}, nil
}

func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volumeID := strings.TrimSpace(req.GetVolumeId())
	stagingPath := strings.TrimSpace(req.GetStagingTargetPath())
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if req.GetVolumeCapability() == nil || req.GetVolumeCapability().GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "mount volume capability is required")
	}
	devicePath := devicePath(req.GetPublishContext(), req.GetVolumeContext())
	if devicePath == "" {
		return nil, status.Error(codes.FailedPrecondition, "Morpheus attach response did not include morpheus.devicePath")
	}
	fsType := filesystemType(req.GetVolumeCapability(), req.GetVolumeContext())
	if err := d.mounter.Stage(ctx, devicePath, stagingPath, fsType, false); err != nil {
		return nil, status.Errorf(codes.Internal, "stage Morpheus volume %q from %s: %v", volumeID, devicePath, err)
	}
	return &csi.NodeStageVolumeResponse{}, nil
}

func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	stagingPath := strings.TrimSpace(req.GetStagingTargetPath())
	if strings.TrimSpace(req.GetVolumeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if err := d.mounter.Unmount(ctx, stagingPath); err != nil {
		return nil, status.Errorf(codes.Internal, "unstage Morpheus volume: %v", err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	stagingPath := strings.TrimSpace(req.GetStagingTargetPath())
	targetPath := strings.TrimSpace(req.GetTargetPath())
	if strings.TrimSpace(req.GetVolumeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if req.GetVolumeCapability() == nil || req.GetVolumeCapability().GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "mount volume capability is required")
	}
	if err := d.mounter.BindMount(ctx, stagingPath, targetPath, req.GetReadonly()); err != nil {
		return nil, status.Errorf(codes.Internal, "publish Morpheus volume: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	targetPath := strings.TrimSpace(req.GetTargetPath())
	if strings.TrimSpace(req.GetVolumeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if err := d.mounter.Unmount(ctx, targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "unpublish Morpheus volume: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func nodeCapability(capability csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
	return &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: capability,
			},
		},
	}
}

type Mounter interface {
	Stage(ctx context.Context, devicePath string, stagingPath string, fsType string, readOnly bool) error
	BindMount(ctx context.Context, source string, target string, readOnly bool) error
	Unmount(ctx context.Context, target string) error
}

type realMounter struct{}

func (realMounter) Stage(ctx context.Context, devicePath string, stagingPath string, fsType string, readOnly bool) error {
	if _, err := os.Stat(devicePath); err != nil {
		return fmtMountError("find attached device", err)
	}
	if err := os.MkdirAll(stagingPath, 0750); err != nil {
		return err
	}
	mounted, err := isMounted(stagingPath)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}
	if !readOnly {
		formatted, err := hasFilesystem(ctx, devicePath)
		if err != nil {
			return err
		}
		if !formatted {
			if err := formatDevice(ctx, devicePath, fsType); err != nil {
				return err
			}
		}
	}
	args := []string{}
	if readOnly {
		args = append(args, "-o", "ro")
	}
	args = append(args, devicePath, stagingPath)
	return run(ctx, "mount", args...)
}

func (realMounter) BindMount(ctx context.Context, source string, target string, readOnly bool) error {
	if err := os.MkdirAll(target, 0750); err != nil {
		return err
	}
	mounted, err := isMounted(target)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}
	if err := run(ctx, "mount", "--bind", source, target); err != nil {
		return err
	}
	if readOnly {
		return run(ctx, "mount", "-o", "remount,bind,ro", target)
	}
	return nil
}

func (realMounter) Unmount(ctx context.Context, target string) error {
	mounted, err := isMounted(target)
	if err != nil {
		return err
	}
	if !mounted {
		_ = os.Remove(target)
		return nil
	}
	if err := run(ctx, "umount", target); err != nil {
		return err
	}
	_ = os.Remove(target)
	return nil
}

func devicePath(maps ...map[string]string) string {
	for _, values := range maps {
		for _, key := range []string{"morpheus.devicePath", "devicePath"} {
			if value := strings.TrimSpace(values[key]); value != "" {
				return value
			}
		}
	}
	return ""
}

func filesystemType(capability *csi.VolumeCapability, volumeContext map[string]string) string {
	if fsType := strings.TrimSpace(capability.GetMount().GetFsType()); fsType != "" {
		return fsType
	}
	if fsType := strings.TrimSpace(volumeContext["csi.storage.k8s.io/fstype"]); fsType != "" {
		return fsType
	}
	return "ext4"
}

func isMounted(target string) (bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cleanTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		cleanTarget = filepath.Clean(target)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[4] == cleanTarget {
			return true, nil
		}
	}
	return false, nil
}
