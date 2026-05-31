package driver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/da3m0nsec/morpheus-csi/internal/config"
	"github.com/da3m0nsec/morpheus-csi/internal/morpheus"
	"google.golang.org/grpc"
)

type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedControllerServer
	csi.UnimplementedNodeServer

	cfg    config.Config
	logger *log.Logger

	volumes   morpheus.InstanceVolumeClient
	discovery morpheus.StorageDiscoveryClient
	mounter   Mounter
}

func New(cfg config.Config, logger *log.Logger) *Driver {
	var client *morpheus.Client
	if strings.TrimSpace(cfg.MorpheusURL) != "" && strings.TrimSpace(cfg.MorpheusToken) != "" {
		var err error
		client, err = morpheus.NewClient(cfg.MorpheusURL, cfg.MorpheusToken)
		if err != nil && logger != nil {
			logger.Printf("invalid Morpheus client configuration: %v", err)
		}
	}

	return &Driver{
		cfg:       cfg,
		logger:    logger,
		volumes:   client,
		discovery: client,
		mounter:   realMounter{},
	}
}

func NewWithDependencies(
	cfg config.Config,
	logger *log.Logger,
	volumes morpheus.InstanceVolumeClient,
	discovery morpheus.StorageDiscoveryClient,
	mounter Mounter,
) *Driver {
	if mounter == nil {
		mounter = realMounter{}
	}
	return &Driver{
		cfg:       cfg,
		logger:    logger,
		volumes:   volumes,
		discovery: discovery,
		mounter:   mounter,
	}
}

func (d *Driver) Run(ctx context.Context) error {
	listener, cleanup, err := listen(d.cfg.Endpoint)
	if err != nil {
		return err
	}
	defer cleanup()

	server := grpc.NewServer()
	csi.RegisterIdentityServer(server, d)

	switch d.cfg.Mode {
	case config.ModeAll:
		csi.RegisterControllerServer(server, d)
		csi.RegisterNodeServer(server, d)
	case config.ModeController:
		csi.RegisterControllerServer(server, d)
	case config.ModeNode:
		csi.RegisterNodeServer(server, d)
	default:
		return fmt.Errorf("unsupported mode %q", d.cfg.Mode)
	}

	errCh := make(chan error, 1)
	go func() {
		d.logger.Printf("starting %s in %s mode on %s", d.cfg.DriverName, d.cfg.Mode, d.cfg.Endpoint)
		errCh <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		server.GracefulStop()
		return nil
	case err := <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	}
}

func listen(endpoint string) (net.Listener, func(), error) {
	if strings.HasPrefix(endpoint, "unix://") {
		path := strings.TrimPrefix(endpoint, "unix://")
		if path == "" {
			return nil, nil, errors.New("unix endpoint path is empty")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			return nil, nil, fmt.Errorf("create socket directory: %w", err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("remove stale socket: %w", err)
		}
		listener, err := net.Listen("unix", path)
		if err != nil {
			return nil, nil, fmt.Errorf("listen on unix socket: %w", err)
		}
		return listener, func() { _ = os.Remove(path) }, nil
	}

	if strings.HasPrefix(endpoint, "tcp://") {
		addr := strings.TrimPrefix(endpoint, "tcp://")
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, nil, fmt.Errorf("listen on tcp socket: %w", err)
		}
		return listener, func() {}, nil
	}

	return nil, nil, fmt.Errorf("unsupported endpoint %q", endpoint)
}
