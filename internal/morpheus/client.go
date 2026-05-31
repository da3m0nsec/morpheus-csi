package morpheus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ParamStorageServerID     = "morpheus.storageServerId"
	ParamStorageVolumeTypeID = "morpheus.storageVolumeTypeId"
	ParamStorageGroup        = "morpheus.storageGroup"
	ParamConfigPrefix        = "morpheus.config."
)

type StorageVolume struct {
	ID         string
	Name       string
	SizeGiB    int64
	ServerID   string
	TypeID     string
	DevicePath string
}

type StorageServer struct {
	ID       string
	Name     string
	Hostname string
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
	AttachStorageVolume(ctx context.Context, req AttachStorageVolumeRequest) (*StorageVolume, error)
	DetachStorageVolume(ctx context.Context, serverID string, volumeID string) error
}

type StorageDiscoveryClient interface {
	ValidateStorageClass(ctx context.Context, parameters map[string]string) error
	ResolveServerIDForNode(ctx context.Context, nodeID string) (string, error)
}

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

func NewClient(rawURL string, token string) (*Client, error) {
	return NewClientWithHTTPClient(rawURL, token, &http.Client{Timeout: 60 * time.Second})
}

func NewClientWithHTTPClient(rawURL string, token string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("Morpheus URL must include scheme and host")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{baseURL: parsed, token: token, httpClient: httpClient}, nil
}

func (c *Client) ValidateStorageClass(_ context.Context, parameters map[string]string) error {
	if strings.TrimSpace(parameters[ParamStorageServerID]) == "" {
		return fmt.Errorf("storage class parameter %q is required", ParamStorageServerID)
	}
	if strings.TrimSpace(parameters[ParamStorageVolumeTypeID]) == "" {
		return fmt.Errorf("storage class parameter %q is required", ParamStorageVolumeTypeID)
	}
	return nil
}

func (c *Client) CreateStorageVolume(ctx context.Context, req CreateStorageVolumeRequest) (*StorageVolume, error) {
	if err := c.ValidateStorageClass(ctx, req.StorageClass); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("volume name is required")
	}
	if req.SizeGiB <= 0 {
		return nil, errors.New("volume size must be greater than zero")
	}

	existing, err := c.findStorageVolumeByName(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.SizeGiB > 0 && existing.SizeGiB < req.SizeGiB {
			return nil, fmt.Errorf("existing Morpheus volume %q is smaller than requested size", req.Name)
		}
		return existing, nil
	}

	storageServerID := strings.TrimSpace(req.StorageClass[ParamStorageServerID])
	storageVolumeTypeID := strings.TrimSpace(req.StorageClass[ParamStorageVolumeTypeID])
	body := map[string]any{
		"storageVolume": map[string]any{
			"name":                req.Name,
			"size":                req.SizeGiB,
			"sizeGiB":             req.SizeGiB,
			"maxStorage":          req.SizeGiB,
			"storageServerId":     jsonID(storageServerID),
			"storageServer":       map[string]any{"id": jsonID(storageServerID)},
			"storageVolumeTypeId": jsonID(storageVolumeTypeID),
			"type":                map[string]any{"id": jsonID(storageVolumeTypeID)},
			"storageGroup":        req.StorageClass[ParamStorageGroup],
			"config":              storageConfig(req.StorageClass),
		},
	}

	var response map[string]any
	if err := c.do(ctx, http.MethodPost, "/api/storage-volumes", nil, body, &response); err != nil {
		return nil, err
	}
	volume := parseVolume(firstMap(response, "storageVolume", "volume"))
	if volume == nil || volume.ID == "" {
		return nil, fmt.Errorf("Morpheus create response did not include a storage volume id")
	}
	return volume, nil
}

func (c *Client) DeleteStorageVolume(ctx context.Context, volumeID string) error {
	volumeID = strings.TrimSpace(volumeID)
	if volumeID == "" {
		return errors.New("volume id is required")
	}
	err := c.do(ctx, http.MethodDelete, "/api/storage-volumes/"+url.PathEscape(volumeID), nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) AttachStorageVolume(ctx context.Context, req AttachStorageVolumeRequest) (*StorageVolume, error) {
	if strings.TrimSpace(req.ServerID) == "" {
		return nil, errors.New("server id is required")
	}
	if strings.TrimSpace(req.VolumeID) == "" {
		return nil, errors.New("volume id is required")
	}

	path := fmt.Sprintf("/api/servers/%s/volumes/%s/attach", url.PathEscape(req.ServerID), url.PathEscape(req.VolumeID))
	body := map[string]any{
		"volume": map[string]any{
			"id": jsonID(req.VolumeID),
		},
	}
	var response map[string]any
	if err := c.do(ctx, http.MethodPut, path, nil, body, &response); err != nil {
		return nil, err
	}
	if volume := parseVolume(firstMap(response, "storageVolume", "volume")); volume != nil {
		return volume, nil
	}
	return &StorageVolume{ID: req.VolumeID, ServerID: req.ServerID}, nil
}

func (c *Client) DetachStorageVolume(ctx context.Context, serverID string, volumeID string) error {
	if strings.TrimSpace(serverID) == "" {
		return errors.New("server id is required")
	}
	if strings.TrimSpace(volumeID) == "" {
		return errors.New("volume id is required")
	}
	path := fmt.Sprintf("/api/servers/%s/volumes/%s/detach", url.PathEscape(serverID), url.PathEscape(volumeID))
	err := c.do(ctx, http.MethodPut, path, nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) ResolveServerIDForNode(ctx context.Context, nodeID string) (string, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return "", errors.New("node id is required")
	}
	values := url.Values{}
	values.Set("phrase", nodeID)

	var response map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/storage-servers", values, nil, &response); err != nil {
		return "", err
	}
	servers := parseServerList(response)
	var matches []StorageServer
	for _, server := range servers {
		if sameName(server.Name, nodeID) || sameName(server.Hostname, nodeID) {
			matches = append(matches, server)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no Morpheus storage server matched Kubernetes node %q", nodeID)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple Morpheus storage servers matched Kubernetes node %q", nodeID)
	}
	return matches[0].ID, nil
}

type apiError struct {
	statusCode int
	body       string
}

func (e apiError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("Morpheus API returned HTTP %d", e.statusCode)
	}
	return fmt.Sprintf("Morpheus API returned HTTP %d: %s", e.statusCode, e.body)
}

func isNotFound(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.statusCode == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method string, path string, query url.Values, body any, out any) error {
	target := *c.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawQuery = query.Encode()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(c.token) != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError{statusCode: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) findStorageVolumeByName(ctx context.Context, name string) (*StorageVolume, error) {
	values := url.Values{}
	values.Set("name", name)
	values.Set("phrase", name)

	var response map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/storage-volumes", values, nil, &response); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, volume := range parseVolumeList(response) {
		if volume.Name == name {
			return &volume, nil
		}
	}
	return nil, nil
}

func storageConfig(parameters map[string]string) map[string]string {
	config := map[string]string{}
	for key, value := range parameters {
		if strings.HasPrefix(key, ParamConfigPrefix) {
			config[strings.TrimPrefix(key, ParamConfigPrefix)] = value
		}
	}
	return config
}

func jsonID(value string) any {
	if id, err := strconv.ParseInt(value, 10, 64); err == nil {
		return id
	}
	return value
}

func parseVolumeList(response map[string]any) []StorageVolume {
	var volumes []StorageVolume
	for _, key := range []string{"storageVolumes", "volumes", "data"} {
		for _, raw := range asSlice(response[key]) {
			if volume := parseVolume(asMap(raw)); volume != nil {
				volumes = append(volumes, *volume)
			}
		}
	}
	return volumes
}

func parseServerList(response map[string]any) []StorageServer {
	var servers []StorageServer
	for _, key := range []string{"storageServers", "servers", "data"} {
		for _, raw := range asSlice(response[key]) {
			m := asMap(raw)
			id := stringValue(m["id"])
			if id == "" {
				continue
			}
			servers = append(servers, StorageServer{
				ID:       id,
				Name:     stringValue(m["name"]),
				Hostname: stringValue(firstValue(m, "hostname", "hostName", "externalId", "externalID")),
			})
		}
	}
	return servers
}

func parseVolume(m map[string]any) *StorageVolume {
	if len(m) == 0 {
		return nil
	}
	volume := &StorageVolume{
		ID:         stringValue(m["id"]),
		Name:       stringValue(m["name"]),
		SizeGiB:    int64Value(firstValue(m, "sizeGiB", "sizeGB", "sizeGb", "maxStorage", "size")),
		ServerID:   stringValue(firstValue(m, "storageServerId", "serverId")),
		TypeID:     stringValue(firstValue(m, "storageVolumeTypeId", "typeId")),
		DevicePath: stringValue(firstValue(m, "devicePath", "device", "path")),
	}
	if volume.ServerID == "" {
		volume.ServerID = stringValue(asMap(firstValue(m, "storageServer", "server"))["id"])
	}
	if volume.TypeID == "" {
		volume.TypeID = stringValue(asMap(firstValue(m, "type", "storageVolumeType"))["id"])
	}
	if volume.DevicePath == "" {
		volume.DevicePath = stringValue(asMap(firstValue(m, "connectionInfo", "attachment", "mount"))["devicePath"])
	}
	if volume.ID == "" {
		return nil
	}
	return volume
}

func firstMap(m map[string]any, keys ...string) map[string]any {
	return asMap(firstValue(m, keys...))
}

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func asMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func asSlice(value any) []any {
	if typed, ok := value.([]any); ok {
		return typed
	}
	return nil
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case json.Number:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case int:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}

func sameName(left string, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}
