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
	"time"

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
			return nil, status.Errorf(codes.FailedPrecondition, "Morpheus attach response did not include morpheus.devicePath and node discovery failed after repeated SCSI rescans: %v", err)
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
	volumeID := strings.TrimSpace(req.GetVolumeId())
	stagingPath := strings.TrimSpace(req.GetStagingTargetPath())
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume id is required")
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	source, err := d.mounter.MountedSource(ctx, stagingPath)
	if err != nil && d.logger != nil {
		d.logger.Printf("could not resolve mounted source for Morpheus volume %q at %s before unstage: %v", volumeID, stagingPath, err)
	}
	if err := d.mounter.Unmount(ctx, stagingPath); err != nil {
		return nil, status.Errorf(codes.Internal, "unstage Morpheus volume: %v", err)
	}
	if source != "" {
		if err := d.mounter.RemoveDevice(ctx, source); err != nil {
			return nil, status.Errorf(codes.Internal, "remove local Morpheus volume device %q after unstage: %v", source, err)
		}
		if d.logger != nil {
			d.logger.Printf("removed local Morpheus volume %q device %s after unstage", volumeID, source)
		}
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
	MountedSource(ctx context.Context, target string) (string, error)
	Unmount(ctx context.Context, target string) error
	ExpandFilesystem(ctx context.Context, volumePath string, fsType string) error
	RescanDevices(ctx context.Context) error
	DiscoverDevicePath(ctx context.Context, sizeGiB int64) (string, error)
	RemoveDevice(ctx context.Context, devicePath string) error
}

type realMounter struct{}

func (realMounter) Stage(ctx context.Context, devicePath string, stagingPath string, fsType string, readOnly bool) error {
	if err := waitForDevice(ctx, devicePath); err != nil {
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

func (realMounter) MountedSource(ctx context.Context, target string) (string, error) {
	return mountedSource(ctx, target)
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
	return rescanDevices(ctx)
}

func (realMounter) DiscoverDevicePath(ctx context.Context, sizeGiB int64) (string, error) {
	var lastErr error
	for attempt := 0; attempt < deviceDiscoveryAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepDiscoveryInterval(ctx); err != nil {
				return "", err
			}
			if err := rescanDevices(ctx); err != nil {
				lastErr = err
				continue
			}
		}

		candidates, err := candidateBlockDevices(ctx, sizeGiB)
		if err != nil {
			lastErr = err
			continue
		}
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		if len(candidates) > 1 {
			return "", fmt.Errorf("multiple unmounted block device candidates found: %s", strings.Join(candidates, ", "))
		}
		if sizeGiB > 0 {
			lastErr = fmt.Errorf("no unmounted block device found matching %dGiB", sizeGiB)
		} else {
			lastErr = errors.New("no unmounted block device candidates found")
		}
	}
	return "", lastErr
}

func (realMounter) RemoveDevice(ctx context.Context, devicePath string) error {
	deviceName, err := removableBlockDeviceName(devicePath)
	if err != nil {
		return err
	}
	mounted, err := isBlockDeviceMounted(deviceName)
	if err != nil {
		return err
	}
	if mounted {
		return fmt.Errorf("refusing to remove mounted block device /dev/%s", deviceName)
	}
	deletePath := filepath.Join(sysBlockRoot, deviceName, "device", "delete")
	if _, err := os.Stat(deletePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.WriteFile(deletePath, []byte("1\n"), 0200); err != nil {
		return fmtMountError(deletePath, err)
	}
	return settleUdev(ctx)
}

var (
	sysBlockRoot      = "/sys/block"
	devRoot           = "/dev"
	devDiskByPathRoot = "/dev/disk/by-path"
	mountInfoPath     = "/proc/self/mountinfo"
	hasFilesystemFunc = hasFilesystem
)

const (
	deviceDiscoveryAttempts = 12
	deviceDiscoveryInterval = 5 * time.Second
)

func rescanDevices(ctx context.Context) error {
	var errs []error
	if err := rescanSCSIHosts(); err != nil {
		errs = append(errs, err)
	}
	if err := settleUdev(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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

func waitForDevice(ctx context.Context, devicePath string) error {
	var lastErr error
	for attempt := 0; attempt < deviceDiscoveryAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepDiscoveryInterval(ctx); err != nil {
				return err
			}
			if err := rescanDevices(ctx); err != nil {
				lastErr = err
			}
		}
		if _, err := os.Stat(devicePath); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func sleepDiscoveryInterval(ctx context.Context) error {
	timer := time.NewTimer(deviceDiscoveryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func candidateBlockDevices(ctx context.Context, sizeGiB int64) ([]string, error) {
	entries, err := os.ReadDir(sysBlockRoot)
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
		path := filepath.Join(devRoot, name)
		formatted, err := hasFilesystemFunc(ctx, path)
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
	matches, _ := filepath.Glob(filepath.Join(sysBlockRoot, name, name+"*"))
	for _, match := range matches {
		if _, err := os.Stat(filepath.Join(match, "partition")); err == nil {
			return true
		}
	}
	return false
}

func blockDeviceSizeGiB(name string) (int64, error) {
	data, err := os.ReadFile(filepath.Join(sysBlockRoot, name, "size"))
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
	devicePath := filepath.Join(devRoot, name)
	matches, _ := filepath.Glob(filepath.Join(devDiskByPathRoot, "*"))
	for _, match := range matches {
		target, err := filepath.EvalSymlinks(match)
		if err == nil && target == devicePath {
			return match
		}
	}
	return devicePath
}

func isMountedSource(source string) (bool, error) {
	data, err := os.ReadFile(mountInfoPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 10 && sameDevicePath(fields[len(fields)-2], source) {
			return true, nil
		}
	}
	return false, nil
}

func removableBlockDeviceName(devicePath string) (string, error) {
	devicePath = strings.TrimSpace(devicePath)
	if devicePath == "" {
		return "", errors.New("device path is required")
	}
	resolved, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		resolved = filepath.Clean(devicePath)
	}
	devRootClean := filepath.Clean(devRoot)
	if resolved != devRootClean && !strings.HasPrefix(resolved, devRootClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to remove non-device path %q", devicePath)
	}
	name := filepath.Base(resolved)
	if name == "." || name == string(os.PathSeparator) || skipBlockDevice(name) {
		return "", fmt.Errorf("refusing to remove unsafe block device %q", name)
	}
	if _, err := os.Stat(filepath.Join(sysBlockRoot, name, "partition")); err == nil {
		return "", fmt.Errorf("refusing to remove partition device %q", name)
	}
	if hasPartitions(name) {
		return "", fmt.Errorf("refusing to remove partitioned block device %q", name)
	}
	if _, err := os.Stat(filepath.Join(sysBlockRoot, name, "device", "delete")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return name, nil
}

func isBlockDeviceMounted(deviceName string) (bool, error) {
	data, err := os.ReadFile(mountInfoPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	devicePath := filepath.Join(devRoot, deviceName)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		source := fields[len(fields)-2]
		resolvedSource, err := filepath.EvalSymlinks(source)
		if err != nil {
			resolvedSource = filepath.Clean(source)
		}
		if sameDevicePath(resolvedSource, devicePath) || isDevicePartition(resolvedSource, devicePath) {
			return true, nil
		}
	}
	return false, nil
}

func sameDevicePath(left string, right string) bool {
	return resolveDevicePath(left) == resolveDevicePath(right)
}

func resolveDevicePath(value string) string {
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return filepath.Clean(value)
	}
	return resolved
}

func isDevicePartition(source string, devicePath string) bool {
	if !strings.HasPrefix(source, devicePath) || len(source) == len(devicePath) {
		return false
	}
	suffix := strings.TrimPrefix(source, devicePath)
	return (suffix[0] >= '0' && suffix[0] <= '9') || suffix[0] == 'p'
}

func settleUdev(ctx context.Context) error {
	if _, err := exec.LookPath("udevadm"); err != nil {
		return nil
	}
	return run(ctx, "udevadm", "settle")
}

func devicePath(maps ...map[string]string) string {
	for _, values := range maps {
		for _, key := range []string{
			morpheus.VolumeContextDevicePath,
			"morpheus.deviceName",
			"morpheus.device",
			"devicePath",
			"deviceName",
			"device",
		} {
			if value := strings.TrimSpace(values[key]); value != "" {
				return normalizeNodeDevicePath(value)
			}
		}
	}
	return ""
}

func normalizeNodeDevicePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/dev/") {
		return value
	}
	for _, prefix := range []string{"sd", "vd", "xvd", "nvme"} {
		if strings.HasPrefix(value, prefix) {
			return "/dev/" + value
		}
	}
	return value
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
