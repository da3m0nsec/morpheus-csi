package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/da3m0nsec/morpheus-csi/internal/morpheus"
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
			nodeCapability(csi.NodeServiceCapability_RPC_EXPAND_VOLUME),
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
	if err := d.mounter.RescanDevices(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "rescan node devices before staging Morpheus volume %q: %v", volumeID, err)
	}
	devicePath := devicePath(req.GetPublishContext(), req.GetVolumeContext())
	if devicePath == "" {
		discovered, err := d.mounter.DiscoverDevicePath(ctx, volumeSizeGiB(req.GetPublishContext(), req.GetVolumeContext()))
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "Morpheus attach response did not include morpheus.devicePath and node discovery failed after SCSI rescan: %v", err)
		}
		devicePath = discovered
		if d.logger != nil {
			d.logger.Printf("discovered Morpheus volume %q device path %s after SCSI rescan", volumeID, devicePath)
		}
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

func (d *Driver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	volumePath := strings.TrimSpace(req.GetVolumePath())
	if strings.TrimSpace(req.GetVolumeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if volumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}
	if req.GetVolumeCapability() == nil || req.GetVolumeCapability().GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "mount volume capability is required")
	}
	sizeGiB, err := requestedSizeGiB(req.GetCapacityRange())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	fsType := filesystemType(req.GetVolumeCapability(), nil)
	if err := d.mounter.ExpandFilesystem(ctx, volumePath, fsType); err != nil {
		return nil, status.Errorf(codes.Internal, "expand Morpheus volume filesystem at %s: %v", volumePath, err)
	}
	return &csi.NodeExpandVolumeResponse{CapacityBytes: sizeGiB * gibibyte}, nil
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
	ExpandFilesystem(ctx context.Context, volumePath string, fsType string) error
	RescanDevices(ctx context.Context) error
	DiscoverDevicePath(ctx context.Context, sizeGiB int64) (string, error)
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

func (realMounter) ExpandFilesystem(ctx context.Context, volumePath string, fsType string) error {
	switch strings.TrimSpace(fsType) {
	case "", "ext4":
		source, err := mountedSource(ctx, volumePath)
		if err != nil {
			return err
		}
		return run(ctx, "resize2fs", source)
	case "xfs":
		return run(ctx, "xfs_growfs", volumePath)
	default:
		return fmtMountError("expand filesystem", errors.New("unsupported filesystem type "+fsType))
	}
}

func (realMounter) RescanDevices(ctx context.Context) error {
	if err := rescanSCSIHosts(); err != nil {
		return err
	}
	return settleUdev(ctx)
}

func (realMounter) DiscoverDevicePath(ctx context.Context, sizeGiB int64) (string, error) {
	candidates, err := candidateBlockDevices(ctx, sizeGiB)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		if sizeGiB > 0 {
			return "", fmt.Errorf("no unmounted block device found matching %dGiB", sizeGiB)
		}
		return "", errors.New("no unmounted block device candidates found")
	}
	if len(candidates) > 1 {
		return "", fmt.Errorf("multiple unmounted block device candidates found: %s", strings.Join(candidates, ", "))
	}
	return candidates[0], nil
}

func rescanSCSIHosts() error {
	paths, err := filepath.Glob("/sys/class/scsi_host/host*/scan")
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("no SCSI host scan targets found under /sys/class/scsi_host")
	}
	var errs []error
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("- - -\n"), 0200); err != nil {
			errs = append(errs, fmtMountError(path, err))
		}
	}
	return errors.Join(errs...)
}

func candidateBlockDevices(ctx context.Context, sizeGiB int64) ([]string, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}
	var candidates []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || skipBlockDevice(name) {
			continue
		}
		if hasPartitions(name) {
			continue
		}
		if sizeGiB > 0 {
			deviceSizeGiB, err := blockDeviceSizeGiB(name)
			if err != nil || deviceSizeGiB != sizeGiB {
				continue
			}
		}
		path := "/dev/" + name
		formatted, err := hasFilesystem(ctx, path)
		if err != nil {
			return nil, err
		}
		if formatted {
			continue
		}
		if mounted, err := isMountedSource(path); err != nil || mounted {
			if err != nil {
				return nil, err
			}
			continue
		}
		candidates = append(candidates, stableDevicePath(name))
	}
	sort.Strings(candidates)
	return candidates, nil
}

func skipBlockDevice(name string) bool {
	for _, prefix := range []string{"loop", "nbd", "ram", "zram", "dm-", "sr", "fd"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func hasPartitions(name string) bool {
	matches, _ := filepath.Glob("/sys/block/" + name + "/" + name + "*")
	for _, match := range matches {
		if _, err := os.Stat(filepath.Join(match, "partition")); err == nil {
			return true
		}
	}
	return false
}

func blockDeviceSizeGiB(name string) (int64, error) {
	data, err := os.ReadFile("/sys/block/" + name + "/size")
	if err != nil {
		return 0, err
	}
	sectors, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	bytes := sectors * 512
	return (bytes + gibibyte - 1) / gibibyte, nil
}

func stableDevicePath(name string) string {
	matches, _ := filepath.Glob("/dev/disk/by-path/*")
	for _, match := range matches {
		target, err := filepath.EvalSymlinks(match)
		if err == nil && target == "/dev/"+name {
			return match
		}
	}
	return "/dev/" + name
}

func isMountedSource(source string) (bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 10 && fields[len(fields)-2] == source {
			return true, nil
		}
	}
	return false, nil
}

func settleUdev(ctx context.Context) error {
	if _, err := exec.LookPath("udevadm"); err != nil {
		return nil
	}
	return run(ctx, "udevadm", "settle")
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

func volumeSizeGiB(maps ...map[string]string) int64 {
	for _, values := range maps {
		value := strings.TrimSpace(values[morpheus.VolumeContextSizeGiB])
		if value == "" {
			continue
		}
		parsed, _ := strconv.ParseInt(value, 10, 64)
		if parsed > 0 {
			return parsed
		}
	}
	return 0
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
