package driver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func hasFilesystem(ctx context.Context, devicePath string) (bool, error) {
	err := run(ctx, "blkid", "-p", devicePath)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func formatDevice(ctx context.Context, devicePath string, fsType string) error {
	fsType = strings.TrimSpace(fsType)
	if fsType == "" {
		fsType = "ext4"
	}
	switch fsType {
	case "ext4":
		return run(ctx, "mkfs.ext4", "-F", devicePath)
	case "xfs":
		return run(ctx, "mkfs.xfs", "-f", devicePath)
	default:
		return fmt.Errorf("unsupported filesystem type %q", fsType)
	}
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
	}
	return nil
}

func fmtMountError(operation string, err error) error {
	return fmt.Errorf("%s: %w", operation, err)
}
