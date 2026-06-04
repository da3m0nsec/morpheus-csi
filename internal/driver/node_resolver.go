package driver

import (
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
	"strings"
	"time"
)

const (
	serviceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	serviceAccountCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

type NodeServerResolver interface {
	ServerIDForNode(ctx context.Context, nodeName string) (string, error)
	ServerIDs(ctx context.Context) ([]string, error)
}

type kubernetesNodeServerResolver struct {
	baseURL    *url.URL
	tokenPath  string
	labelKey   string
	httpClient *http.Client
}

func newKubernetesNodeServerResolver(labelKey string) (NodeServerResolver, error) {
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	port := firstNonEmptyString(os.Getenv("KUBERNETES_SERVICE_PORT_HTTPS"), os.Getenv("KUBERNETES_SERVICE_PORT"))
	if host == "" || port == "" {
		return nil, errors.New("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are required")
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if data, err := os.ReadFile(serviceAccountCAPath); err == nil {
		_ = pool.AppendCertsFromPEM(data)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool}

	return newKubernetesNodeServerResolverWithHTTPClient(
		"https://"+host+":"+port,
		serviceAccountTokenPath,
		labelKey,
		&http.Client{Timeout: 30 * time.Second, Transport: transport},
	)
}

func newKubernetesNodeServerResolverWithHTTPClient(rawURL string, tokenPath string, labelKey string, httpClient *http.Client) (NodeServerResolver, error) {
	parsed, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("Kubernetes API URL must include scheme and host")
	}
	if strings.TrimSpace(labelKey) == "" {
		return nil, errors.New("node server ID label is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &kubernetesNodeServerResolver{
		baseURL:    parsed,
		tokenPath:  tokenPath,
		labelKey:   strings.TrimSpace(labelKey),
		httpClient: httpClient,
	}, nil
}

func (r *kubernetesNodeServerResolver) ServerIDForNode(ctx context.Context, nodeName string) (string, error) {
	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return "", errors.New("node name is required")
	}
	var node kubernetesNode
	if err := r.do(ctx, "/api/v1/nodes/"+url.PathEscape(nodeName), &node); err != nil {
		return "", err
	}
	serverID := strings.TrimSpace(node.Metadata.Labels[r.labelKey])
	if serverID == "" {
		return "", fmt.Errorf("node %q is missing label %q", nodeName, r.labelKey)
	}
	return serverID, nil
}

func (r *kubernetesNodeServerResolver) ServerIDs(ctx context.Context) ([]string, error) {
	var list kubernetesNodeList
	if err := r.do(ctx, "/api/v1/nodes", &list); err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var ids []string
	for _, node := range list.Items {
		serverID := strings.TrimSpace(node.Metadata.Labels[r.labelKey])
		if serverID == "" {
			continue
		}
		if _, ok := seen[serverID]; ok {
			continue
		}
		seen[serverID] = struct{}{}
		ids = append(ids, serverID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no Kubernetes nodes have label %q", r.labelKey)
	}
	return ids, nil
}

func (r *kubernetesNodeServerResolver) do(ctx context.Context, path string, out any) error {
	target := *r.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	token, err := os.ReadFile(r.tokenPath)
	if err != nil {
		return fmt.Errorf("read Kubernetes service account token: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("Kubernetes API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

type kubernetesNode struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}

type kubernetesNodeList struct {
	Items []kubernetesNode `json:"items"`
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
