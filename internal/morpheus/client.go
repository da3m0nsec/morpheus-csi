package morpheus

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ParamServerID              = "morpheus.serverId"
	legacyParamHostID          = "morpheus.hostId"
	legacyParamInstanceID      = "morpheus.instanceId"
	ParamStorageTypeID         = "morpheus.storageTypeId"
	ParamDatastoreID           = "morpheus.datastoreId"
	ParamDeleteOriginalVolumes = "morpheus.deleteOriginalVolumes"
	ParamConfigPrefix          = "morpheus.config."

	VolumeContextServerID   = "morpheus.serverId"
	VolumeContextVolumeName = "morpheus.volumeName"
	VolumeContextDevicePath = "morpheus.devicePath"
)

type StorageVolume struct {
	ID            string
	Name          string
	SizeGiB       int64
	RootVolume    bool
	StorageTypeID string
	DatastoreID   string
	DevicePath    string
}

type VolumeRef struct {
	ServerID string
	VolumeID string
}

type ResizeVolumeRequest struct {
	Name         string
	SizeGiB      int64
	StorageClass map[string]string
	VolumeID     string
}

type ServerVolumeClient interface {
	EnsureVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error)
	DeleteVolume(ctx context.Context, ref VolumeRef) error
	ExpandVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error)
	GetVolume(ctx context.Context, ref VolumeRef) (*StorageVolume, error)
}

type StorageDiscoveryClient interface {
	ValidateStorageClass(ctx context.Context, parameters map[string]string) error
}

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

func NewClient(rawURL string, token string) (*Client, error) {
	return NewClientWithHTTPClient(rawURL, token, &http.Client{Timeout: 60 * time.Second})
}

func NewClientWithTLS(rawURL string, token string, caFile string, insecureSkipVerify bool) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := &tls.Config{InsecureSkipVerify: insecureSkipVerify} //nolint:gosec // Lab clusters may use self-signed Morpheus certificates.

	if strings.TrimSpace(caFile) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		data, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read Morpheus CA file: %w", err)
		}
		if ok := pool.AppendCertsFromPEM(data); !ok {
			return nil, fmt.Errorf("Morpheus CA file %q did not contain a PEM certificate", caFile)
		}
		tlsConfig.RootCAs = pool
	}

	transport.TLSClientConfig = tlsConfig
	return NewClientWithHTTPClient(rawURL, token, &http.Client{
		Timeout:   60 * time.Second,
		Transport: transport,
	})
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

func EncodeVolumeID(serverID string, volumeID string) string {
	return strings.TrimSpace(serverID) + ":" + strings.TrimSpace(volumeID)
}

func DecodeVolumeID(volumeID string) (VolumeRef, error) {
	parts := strings.Split(strings.TrimSpace(volumeID), ":")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return VolumeRef{}, fmt.Errorf("volume id must have format <serverID>:<volumeID>")
	}
	return VolumeRef{ServerID: strings.TrimSpace(parts[0]), VolumeID: strings.TrimSpace(parts[1])}, nil
}

func (c *Client) ValidateStorageClass(_ context.Context, parameters map[string]string) error {
	if StorageClassServerID(parameters) == "" {
		return fmt.Errorf("storage class parameter %q is required", ParamServerID)
	}
	return nil
}

func (c *Client) EnsureVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error) {
	if err := c.validateResizeRequest(ctx, req); err != nil {
		return nil, err
	}

	serverID := StorageClassServerID(req.StorageClass)
	volumes, err := c.getServerVolumes(ctx, serverID)
	if err != nil {
		return nil, err
	}

	if existing := findVolumeByName(volumes, req.Name); existing != nil {
		if existing.SizeGiB >= req.SizeGiB {
			return existing, nil
		}
		return c.resizeVolume(ctx, serverID, volumes, existing.ID, req)
	}
	return c.resizeVolume(ctx, serverID, volumes, "", req)
}

func (c *Client) DeleteVolume(ctx context.Context, ref VolumeRef) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	volumes, err := c.getServerVolumes(ctx, ref.ServerID)
	if err != nil {
		return err
	}
	target := findVolumeByID(volumes, ref.VolumeID)
	if target == nil {
		return nil
	}
	if target.RootVolume {
		return fmt.Errorf("refusing to delete root volume %q", ref.VolumeID)
	}
	next := make([]StorageVolume, 0, len(volumes)-1)
	for _, volume := range volumes {
		if volume.ID != ref.VolumeID {
			next = append(next, volume)
		}
	}
	return c.resizeServer(ctx, ref.ServerID, next, nil)
}

func (c *Client) ExpandVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error) {
	if err := c.validateResizeRequest(ctx, req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.VolumeID) == "" {
		return nil, errors.New("volume id is required")
	}
	serverID := StorageClassServerID(req.StorageClass)
	volumes, err := c.getServerVolumes(ctx, serverID)
	if err != nil {
		return nil, err
	}
	existing := findVolumeByID(volumes, req.VolumeID)
	if existing == nil {
		return nil, fmt.Errorf("volume %q not found on server %q", req.VolumeID, serverID)
	}
	if existing.RootVolume {
		return nil, fmt.Errorf("refusing to resize root volume %q", req.VolumeID)
	}
	if existing.SizeGiB > req.SizeGiB {
		return nil, fmt.Errorf("cannot shrink volume %q from %dGiB to %dGiB", req.VolumeID, existing.SizeGiB, req.SizeGiB)
	}
	if existing.SizeGiB == req.SizeGiB {
		return existing, nil
	}
	return c.resizeVolume(ctx, serverID, volumes, req.VolumeID, req)
}

func (c *Client) GetVolume(ctx context.Context, ref VolumeRef) (*StorageVolume, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	volumes, err := c.getServerVolumes(ctx, ref.ServerID)
	if err != nil {
		return nil, err
	}
	volume := findVolumeByID(volumes, ref.VolumeID)
	if volume == nil {
		return nil, fmt.Errorf("volume %q not found on server %q", ref.VolumeID, ref.ServerID)
	}
	return volume, nil
}

func (c *Client) validateResizeRequest(ctx context.Context, req ResizeVolumeRequest) error {
	if err := c.ValidateStorageClass(ctx, req.StorageClass); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) == "" && strings.TrimSpace(req.VolumeID) == "" {
		return errors.New("volume name or volume id is required")
	}
	if req.SizeGiB <= 0 {
		return errors.New("volume size must be greater than zero")
	}
	return nil
}

func validateRef(ref VolumeRef) error {
	if strings.TrimSpace(ref.ServerID) == "" {
		return errors.New("server id is required")
	}
	if strings.TrimSpace(ref.VolumeID) == "" {
		return errors.New("volume id is required")
	}
	return nil
}

func (c *Client) resizeVolume(ctx context.Context, serverID string, volumes []StorageVolume, volumeID string, req ResizeVolumeRequest) (*StorageVolume, error) {
	found := false
	next := make([]StorageVolume, 0, len(volumes)+1)
	for _, volume := range volumes {
		if volume.ID == volumeID || (volumeID == "" && volume.Name == req.Name) {
			volume.Name = firstNonEmpty(req.Name, volume.Name)
			volume.SizeGiB = req.SizeGiB
			volume.StorageTypeID = firstNonEmpty(volume.StorageTypeID, req.StorageClass[ParamStorageTypeID])
			volume.DatastoreID = firstNonEmpty(volume.DatastoreID, req.StorageClass[ParamDatastoreID])
			found = true
		}
		next = append(next, volume)
	}
	if !found {
		next = append(next, StorageVolume{
			Name:          req.Name,
			SizeGiB:       req.SizeGiB,
			RootVolume:    false,
			StorageTypeID: req.StorageClass[ParamStorageTypeID],
			DatastoreID:   req.StorageClass[ParamDatastoreID],
		})
	}
	if err := c.resizeServer(ctx, serverID, next, req.StorageClass); err != nil {
		return nil, err
	}

	refreshed, err := c.getServerVolumes(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if volumeID != "" {
		if volume := findVolumeByID(refreshed, volumeID); volume != nil {
			return volume, nil
		}
	}
	if volume := findVolumeByName(refreshed, req.Name); volume != nil {
		return volume, nil
	}
	return nil, fmt.Errorf("Morpheus resize completed but volume %q was not found", req.Name)
}

func (c *Client) resizeServer(ctx context.Context, serverID string, volumes []StorageVolume, parameters map[string]string) error {
	body := map[string]any{
		"server": map[string]any{
			"volumes":               resizeVolumesPayload(volumes),
			"deleteOriginalVolumes": deleteOriginalVolumes(parameters),
		},
	}
	return c.do(ctx, http.MethodPut, "/api/servers/"+url.PathEscape(serverID)+"/resize", nil, body, nil)
}

func (c *Client) getServerVolumes(ctx context.Context, serverID string) ([]StorageVolume, error) {
	values := url.Values{}
	values.Set("details", "true")

	var response map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/servers/"+url.PathEscape(serverID), values, nil, &response); err != nil {
		return nil, err
	}
	server := firstMap(response, "server")
	if len(server) == 0 {
		server = response
	}
	return parseVolumeList(server), nil
}

func resizeVolumesPayload(volumes []StorageVolume) []map[string]any {
	payload := make([]map[string]any, 0, len(volumes))
	for _, volume := range volumes {
		item := map[string]any{
			"name":       volume.Name,
			"size":       volume.SizeGiB,
			"sizeGiB":    volume.SizeGiB,
			"maxStorage": volume.SizeGiB,
			"rootVolume": volume.RootVolume,
		}
		if volume.ID != "" {
			item["id"] = jsonID(volume.ID)
		}
		if volume.StorageTypeID != "" {
			item["storageType"] = jsonID(volume.StorageTypeID)
			item["storageTypeId"] = jsonID(volume.StorageTypeID)
		}
		if volume.DatastoreID != "" {
			item["datastoreId"] = jsonID(volume.DatastoreID)
			item["datastore"] = map[string]any{"id": jsonID(volume.DatastoreID)}
		}
		payload = append(payload, item)
	}
	return payload
}

func deleteOriginalVolumes(parameters map[string]string) bool {
	if parameters == nil {
		return false
	}
	value, _ := strconv.ParseBool(strings.TrimSpace(parameters[ParamDeleteOriginalVolumes]))
	return value
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

func parseVolumeList(server map[string]any) []StorageVolume {
	for _, key := range []string{"volumes", "storageVolumes", "disks"} {
		if rawVolumes := asSlice(server[key]); len(rawVolumes) > 0 {
			volumes := make([]StorageVolume, 0, len(rawVolumes))
			for _, raw := range rawVolumes {
				if volume := parseVolume(asMap(raw)); volume != nil {
					volumes = append(volumes, *volume)
				}
			}
			return volumes
		}
	}
	return nil
}

func StorageClassServerID(parameters map[string]string) string {
	if parameters == nil {
		return ""
	}
	if serverID := strings.TrimSpace(parameters[ParamServerID]); serverID != "" {
		return serverID
	}
	if hostID := strings.TrimSpace(parameters[legacyParamHostID]); hostID != "" {
		return hostID
	}
	return strings.TrimSpace(parameters[legacyParamInstanceID])
}

func parseVolume(m map[string]any) *StorageVolume {
	if len(m) == 0 {
		return nil
	}
	volume := &StorageVolume{
		ID:            stringValue(m["id"]),
		Name:          stringValue(m["name"]),
		SizeGiB:       int64Value(firstValue(m, "sizeGiB", "sizeGB", "sizeGb", "maxStorage", "size")),
		RootVolume:    boolValue(firstValue(m, "rootVolume", "root", "isRoot")),
		StorageTypeID: stringValue(firstValue(m, "storageTypeId", "storageType")),
		DatastoreID:   stringValue(firstValue(m, "datastoreId")),
		DevicePath:    stringValue(firstValue(m, "devicePath", "device", "path")),
	}
	if volume.StorageTypeID == "" {
		volume.StorageTypeID = stringValue(asMap(firstValue(m, "storageType", "type"))["id"])
	}
	if volume.DatastoreID == "" {
		volume.DatastoreID = stringValue(asMap(firstValue(m, "datastore"))["id"])
	}
	if volume.DevicePath == "" {
		volume.DevicePath = stringValue(asMap(firstValue(m, "connectionInfo", "attachment", "mount"))["devicePath"])
	}
	if volume.ID == "" {
		return nil
	}
	return volume
}

func findVolumeByName(volumes []StorageVolume, name string) *StorageVolume {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}
	return nil
}

func findVolumeByID(volumes []StorageVolume, id string) *StorageVolume {
	for i := range volumes {
		if volumes[i].ID == id {
			return &volumes[i]
		}
	}
	return nil
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

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func jsonID(value string) any {
	if id, err := strconv.ParseInt(value, 10, 64); err == nil {
		return id
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
