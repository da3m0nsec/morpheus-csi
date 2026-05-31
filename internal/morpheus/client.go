package morpheus

import "context"

type StorageVolume struct {
	ID       string
	Name     string
	SizeGiB  int64
	ServerID string
	TypeID   string
}

type CreateStorageVolumeRequest struct {
	Name         string
	SizeGiB      int64
	StorageClass map[string]string
}

type AttachStorageVolumeRequest struct {
	ServerID string
	VolumeID string
	NodeID   string
}

type StorageVolumeClient interface {
	CreateStorageVolume(ctx context.Context, req CreateStorageVolumeRequest) (*StorageVolume, error)
	DeleteStorageVolume(ctx context.Context, volumeID string) error
}

type StorageAttachmentClient interface {
	AttachStorageVolume(ctx context.Context, req AttachStorageVolumeRequest) error
	DetachStorageVolume(ctx context.Context, serverID string, volumeID string) error
}

type StorageDiscoveryClient interface {
	ValidateStorageClass(ctx context.Context, parameters map[string]string) error
}
