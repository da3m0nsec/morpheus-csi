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
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ParamServerID         = "morpheus.serverId"
	legacyParamHostID     = "morpheus.hostId"
	legacyParamInstanceID = "morpheus.instanceId"
	ParamStorageType      = "morpheus.storageType"
	ParamStorageTypeID    = "morpheus.storageTypeId"
	ParamDatastoreID      = "morpheus.datastoreId"
	ParamConfigPrefix     = "morpheus.config."

	VolumeContextServerID   = "morpheus.serverId"
	VolumeContextVolumeID   = "morpheus.volumeId"
	VolumeContextVolumeName = "morpheus.volumeName"
	VolumeContextDevicePath = "morpheus.devicePath"
	VolumeContextSizeGiB    = "morpheus.sizeGiB"

	defaultStorageType = "38"
	bytesPerGiB        = 1024 * 1024 * 1024

	postResizeVolumeLookupAttempts = 6
	postResizeVolumeLookupInterval = 2 * time.Second
)

type StorageVolume struct {
	ID                   string
	Name                 string
	SizeGiB              int64
	RootVolume           bool
	StorageTypeID        string
	DatastoreID          string
	ControllerMountPoint string
	DevicePath           string
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

type MoveVolumeRequest struct {
	VolumeID           string
	SourceServerID     string
	TargetServerID     string
	CandidateServerIDs []string
}

type ServerVolumeClient interface {
	EnsureVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error)
	DeleteVolume(ctx context.Context, ref VolumeRef) error
	ExpandVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error)
	GetVolume(ctx context.Context, ref VolumeRef) (*StorageVolume, error)
	FindVolume(ctx context.Context, volumeID string, serverIDs []string) (VolumeRef, *StorageVolume, error)
	MoveVolume(ctx context.Context, req MoveVolumeRequest) (*StorageVolume, error)
	DetachVolume(ctx context.Context, ref VolumeRef) error
}

type StorageDiscoveryClient interface {
	ValidateStorageClass(ctx context.Context, parameters map[string]string) error
}

type NodeServerLookupClient interface {
	ResolveServerIDByNodeName(ctx context.Context, nodeName string) (string, error)
}

type serverState struct {
	ID      string
	Volumes []StorageVolume
}

type serverRecord struct {
	ID         string
	Name       string
	Hostname   string
	ExternalID string
}

type Client struct {
	baseURL     *url.URL
	token       string
	httpClient  *http.Client
	debugLogger *log.Logger
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

func (c *Client) SetDebugLogger(logger *log.Logger) {
	c.debugLogger = logger
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

func (c *Client) ResolveServerIDByNodeName(ctx context.Context, nodeName string) (string, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return "", errors.New("node name is required")
	}

	values := url.Values{}
	values.Set("phrase", nodeName)

	var response map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/servers", values, nil, &response); err != nil {
		return "", err
	}
	servers := parseServerList(response)
	if len(servers) == 0 {
		return "", fmt.Errorf("no Morpheus server matched Kubernetes node %q", nodeName)
	}

	var exactMatches []serverRecord
	for _, server := range servers {
		if server.matchesNodeName(nodeName) {
			exactMatches = append(exactMatches, server)
		}
	}
	switch {
	case len(exactMatches) == 1:
		return exactMatches[0].ID, nil
	case len(exactMatches) > 1:
		return "", fmt.Errorf("multiple Morpheus servers exactly matched Kubernetes node %q", nodeName)
	case len(servers) == 1:
		return servers[0].ID, nil
	default:
		return "", fmt.Errorf("multiple Morpheus servers matched Kubernetes node %q; add node label to disambiguate", nodeName)
	}
}

func (c *Client) EnsureVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error) {
	if err := c.validateResizeRequest(ctx, req); err != nil {
		return nil, err
	}

	serverID := StorageClassServerID(req.StorageClass)
	state, err := c.getServerState(ctx, serverID)
	if err != nil {
		return nil, err
	}

	volumes := state.Volumes
	if existing := findVolumeByName(volumes, req.Name); existing != nil {
		if existing.SizeGiB >= req.SizeGiB {
			return existing, nil
		}
		return c.resizeVolume(ctx, state, existing.ID, req)
	}
	return c.resizeVolume(ctx, state, "", req)
}

func (c *Client) DeleteVolume(ctx context.Context, ref VolumeRef) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	state, err := c.getServerState(ctx, ref.ServerID)
	if err != nil {
		return err
	}
	volumes := state.Volumes
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
	return c.resizeServer(ctx, state.ID, next)
}

func (c *Client) FindVolume(ctx context.Context, volumeID string, serverIDs []string) (VolumeRef, *StorageVolume, error) {
	volumeID = strings.TrimSpace(volumeID)
	if volumeID == "" {
		return VolumeRef{}, nil, errors.New("volume id is required")
	}
	for _, serverID := range uniqueNonEmpty(serverIDs) {
		volumes, err := c.getServerVolumes(ctx, serverID)
		if err != nil {
			return VolumeRef{}, nil, err
		}
		if volume := findVolumeByID(volumes, volumeID); volume != nil {
			return VolumeRef{ServerID: serverID, VolumeID: volumeID}, volume, nil
		}
	}
	return VolumeRef{}, nil, fmt.Errorf("volume %q was not found on candidate Morpheus servers", volumeID)
}

func (c *Client) MoveVolume(ctx context.Context, req MoveVolumeRequest) (*StorageVolume, error) {
	if strings.TrimSpace(req.TargetServerID) == "" {
		return nil, errors.New("target server id is required")
	}
	sourceRef, volume, err := c.FindVolume(ctx, req.VolumeID, append(req.CandidateServerIDs, req.SourceServerID, req.TargetServerID))
	if err != nil {
		return nil, err
	}
	if sourceRef.ServerID == req.TargetServerID {
		return volume, nil
	}
	if volume.RootVolume {
		return nil, fmt.Errorf("refusing to move root volume %q", req.VolumeID)
	}

	if err := c.DetachVolume(ctx, sourceRef); err != nil {
		return nil, err
	}
	targetRef := VolumeRef{ServerID: req.TargetServerID, VolumeID: req.VolumeID}
	if err := c.attachVolume(ctx, targetRef); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < postResizeVolumeLookupAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(postResizeVolumeLookupInterval):
			}
		}
		moved, err := c.GetVolume(ctx, targetRef)
		if err == nil {
			return moved, nil
		}
	}
	return nil, fmt.Errorf("Morpheus attach completed but volume %q was not found on server %q", req.VolumeID, req.TargetServerID)
}

func (c *Client) DetachVolume(ctx context.Context, ref VolumeRef) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/api/servers/"+url.PathEscape(ref.ServerID)+"/volumes/"+url.PathEscape(ref.VolumeID)+"/detach", nil, nil, nil)
}

func (c *Client) attachVolume(ctx context.Context, ref VolumeRef) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/api/servers/"+url.PathEscape(ref.ServerID)+"/volumes/"+url.PathEscape(ref.VolumeID)+"/attach", nil, nil, nil)
}

func (c *Client) ExpandVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error) {
	if err := c.validateResizeRequest(ctx, req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.VolumeID) == "" {
		return nil, errors.New("volume id is required")
	}
	serverID := StorageClassServerID(req.StorageClass)
	state, err := c.getServerState(ctx, serverID)
	if err != nil {
		return nil, err
	}
	volumes := state.Volumes
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
	return c.resizeVolume(ctx, state, req.VolumeID, req)
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

func (c *Client) resizeVolume(ctx context.Context, state serverState, volumeID string, req ResizeVolumeRequest) (*StorageVolume, error) {
	volumes := state.Volumes
	found := false
	next := make([]StorageVolume, 0, len(volumes)+1)
	for _, volume := range volumes {
		if volume.ID == volumeID || (volumeID == "" && volume.Name == req.Name) {
			volume.Name = firstNonEmpty(req.Name, volume.Name)
			volume.SizeGiB = req.SizeGiB
			volume.StorageTypeID = firstNonEmpty(volume.StorageTypeID, storageClassStorageType(req.StorageClass))
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
			StorageTypeID: storageClassStorageType(req.StorageClass),
			DatastoreID:   req.StorageClass[ParamDatastoreID],
		})
	}
	if err := c.resizeServer(ctx, state.ID, next); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < postResizeVolumeLookupAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(postResizeVolumeLookupInterval):
			}
		}

		refreshed, err := c.getServerVolumes(ctx, state.ID)
		if err != nil {
			return nil, err
		}
		if volume := findResizedVolume(volumes, refreshed, volumeID, req); volume != nil {
			return volume, nil
		}
	}
	return nil, fmt.Errorf("Morpheus resize completed but volume %q was not found", req.Name)
}

func (c *Client) resizeServer(ctx context.Context, serverID string, volumes []StorageVolume) error {
	body := map[string]any{
		"volumes": resizeVolumesPayload(volumes),
	}
	return c.do(ctx, http.MethodPut, "/api/servers/"+url.PathEscape(serverID)+"/resize", nil, body, nil)
}

func (c *Client) getServerVolumes(ctx context.Context, serverID string) ([]StorageVolume, error) {
	state, err := c.getServerState(ctx, serverID)
	if err != nil {
		return nil, err
	}
	return state.Volumes, nil
}

func (c *Client) getServerState(ctx context.Context, serverID string) (serverState, error) {
	values := url.Values{}
	values.Set("details", "true")

	var response map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/servers/"+url.PathEscape(serverID), values, nil, &response); err != nil {
		return serverState{}, err
	}
	server := firstMap(response, "server")
	if len(server) == 0 {
		server = response
	}
	state := serverState{
		ID:      firstNonEmpty(stringValue(server["id"]), serverID),
		Volumes: parseVolumeList(server),
	}
	return state, nil
}

func resizeVolumesPayload(volumes []StorageVolume) []map[string]any {
	payload := make([]map[string]any, 0, len(volumes))
	for _, volume := range volumes {
		item := map[string]any{
			"name":       volume.Name,
			"size":       volume.SizeGiB,
			"sizeId":     nil,
			"rootVolume": volume.RootVolume,
		}
		if volume.ID != "" {
			item["id"] = jsonID(volume.ID)
		} else if !volume.RootVolume {
			item["id"] = -1
		}
		if volume.StorageTypeID != "" {
			item["storageType"] = jsonID(volume.StorageTypeID)
		}
		if volume.DatastoreID != "" {
			item["datastoreId"] = jsonID(volume.DatastoreID)
		}
		if volume.ControllerMountPoint != "" {
			item["controllerMountPoint"] = volume.ControllerMountPoint
		}
		payload = append(payload, item)
	}
	return payload
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
	var encodedBody []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		encodedBody = encoded
		reader = bytes.NewReader(encodedBody)
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
	c.logHTTP(method, target, resp.StatusCode, encodedBody, data)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError{statusCode: resp.StatusCode, body: strings.TrimSpace(string(data))}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func (c *Client) logHTTP(method string, target url.URL, statusCode int, requestBody []byte, responseBody []byte) {
	if c.debugLogger == nil {
		return
	}
	request := strings.TrimSpace(string(requestBody))
	response := strings.TrimSpace(string(responseBody))
	if request == "" {
		request = "<empty>"
	}
	if response == "" {
		response = "<empty>"
	}
	c.debugLogger.Printf(
		"morpheus api %s %s status=%d request=%s response=%s",
		method,
		target.RequestURI(),
		statusCode,
		request,
		response,
	)
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

func parseServerList(response map[string]any) []serverRecord {
	for _, key := range []string{"servers", "storageServers", "data"} {
		if rawServers := asSlice(response[key]); len(rawServers) > 0 {
			servers := make([]serverRecord, 0, len(rawServers))
			for _, raw := range rawServers {
				if server := parseServer(asMap(raw)); server.ID != "" {
					servers = append(servers, server)
				}
			}
			return servers
		}
	}
	if server := parseServer(firstMap(response, "server")); server.ID != "" {
		return []serverRecord{server}
	}
	return nil
}

func parseServer(values map[string]any) serverRecord {
	if len(values) == 0 {
		return serverRecord{}
	}
	return serverRecord{
		ID:         stringValue(values["id"]),
		Name:       stringValue(values["name"]),
		Hostname:   stringValue(firstValue(values, "hostname", "hostName", "displayName")),
		ExternalID: stringValue(firstValue(values, "externalId", "externalID", "uuid")),
	}
}

func (s serverRecord) matchesNodeName(nodeName string) bool {
	nodeName = strings.TrimSpace(nodeName)
	for _, candidate := range []string{s.Name, s.Hostname, s.ExternalID} {
		if strings.EqualFold(strings.TrimSpace(candidate), nodeName) {
			return true
		}
	}
	return false
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

func storageClassStorageType(parameters map[string]string) string {
	if parameters == nil {
		return defaultStorageType
	}
	if value := strings.TrimSpace(parameters[ParamStorageType]); value != "" {
		return value
	}
	if value := strings.TrimSpace(parameters[ParamStorageTypeID]); value != "" {
		return value
	}
	return defaultStorageType
}

func parseVolume(m map[string]any) *StorageVolume {
	if len(m) == 0 {
		return nil
	}
	volume := &StorageVolume{
		ID:                   stringValue(m["id"]),
		Name:                 stringValue(m["name"]),
		SizeGiB:              storageSizeGiB(m),
		RootVolume:           boolValue(firstValue(m, "rootVolume", "root", "isRoot")),
		StorageTypeID:        stringValue(firstValue(m, "storageTypeId", "storageType")),
		DatastoreID:          stringValue(firstValue(m, "datastoreId")),
		ControllerMountPoint: stringValue(m["controllerMountPoint"]),
		DevicePath:           normalizeDevicePath(stringValue(firstValue(m, "devicePath", "deviceName", "device", "path"))),
	}
	if volume.StorageTypeID == "" {
		volume.StorageTypeID = stringValue(asMap(firstValue(m, "storageType", "type"))["id"])
	}
	if volume.DatastoreID == "" {
		volume.DatastoreID = stringValue(asMap(firstValue(m, "datastore"))["id"])
	}
	if volume.DevicePath == "" {
		volume.DevicePath = normalizeDevicePath(stringValue(asMap(firstValue(m, "connectionInfo", "attachment", "mount"))["devicePath"]))
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

func findResizedVolume(before []StorageVolume, after []StorageVolume, volumeID string, req ResizeVolumeRequest) *StorageVolume {
	if volumeID != "" {
		return findVolumeByID(after, volumeID)
	}
	if volume := findVolumeByName(after, req.Name); volume != nil {
		return volume
	}
	return findNewVolume(before, after, req)
}

func findNewVolume(before []StorageVolume, after []StorageVolume, req ResizeVolumeRequest) *StorageVolume {
	existingIDs := make(map[string]struct{}, len(before))
	for _, volume := range before {
		if volume.ID != "" {
			existingIDs[volume.ID] = struct{}{}
		}
	}

	expectedStorageType := storageClassStorageType(req.StorageClass)
	expectedDatastoreID := strings.TrimSpace(req.StorageClass[ParamDatastoreID])
	for i := range after {
		volume := &after[i]
		if volume.ID == "" || volume.RootVolume {
			continue
		}
		if _, ok := existingIDs[volume.ID]; ok {
			continue
		}
		if volume.SizeGiB > 0 && volume.SizeGiB < req.SizeGiB {
			continue
		}
		if expectedStorageType != "" && volume.StorageTypeID != "" && volume.StorageTypeID != expectedStorageType {
			continue
		}
		if expectedDatastoreID != "" && volume.DatastoreID != "" && volume.DatastoreID != expectedDatastoreID {
			continue
		}
		return volume
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

func storageSizeGiB(values map[string]any) int64 {
	if value := int64Value(firstValue(values, "sizeGiB", "sizeGB", "sizeGb")); value > 0 {
		return value
	}
	value := int64Value(firstValue(values, "maxStorage", "size"))
	if value > bytesPerGiB {
		return (value + bytesPerGiB - 1) / bytesPerGiB
	}
	return value
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

func normalizeDevicePath(value string) string {
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

func uniqueNonEmpty(values []string) []string {
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
