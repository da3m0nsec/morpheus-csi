package config

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ModeAll        = "all"
	ModeController = "controller"
	ModeNode       = "node"
)

type Config struct {
	Endpoint        string
	DriverName      string
	Mode            string
	NodeID          string
	KubeletRootPath string
	LogLevel        string
	MorpheusURL     string
	MorpheusToken   string
}

func Default() Config {
	return Config{
		Endpoint:        "unix:///csi/csi.sock",
		DriverName:      "csi.morpheusdata.com",
		Mode:            ModeAll,
		KubeletRootPath: "/var/lib/kubelet",
		LogLevel:        "info",
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("endpoint is required")
	}
	if strings.TrimSpace(c.DriverName) == "" {
		return errors.New("driver name is required")
	}

	switch c.Mode {
	case ModeAll, ModeController:
	case ModeNode:
		if strings.TrimSpace(c.NodeID) == "" {
			return errors.New("node-id is required in node mode")
		}
	default:
		return fmt.Errorf("unsupported mode %q", c.Mode)
	}

	return nil
}
