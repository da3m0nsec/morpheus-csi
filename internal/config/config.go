package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	ModeAll        = "all"
	ModeController = "controller"
	ModeNode       = "node"
)

type Config struct {
	Endpoint                   string
	DriverName                 string
	Mode                       string
	NodeID                     string
	KubeletRootPath            string
	LogLevel                   string
	MorpheusURL                string
	MorpheusToken              string
	MorpheusCAFile             string
	MorpheusInsecureSkipVerify bool
	MorpheusDebug              bool
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
		if strings.TrimSpace(c.MorpheusURL) == "" {
			return errors.New("MORPHEUS_URL is required in controller mode")
		}
		if _, err := url.ParseRequestURI(c.MorpheusURL); err != nil {
			return fmt.Errorf("MORPHEUS_URL is invalid: %w", err)
		}
		if strings.TrimSpace(c.MorpheusToken) == "" {
			return errors.New("MORPHEUS_TOKEN is required in controller mode")
		}
	case ModeNode:
		if strings.TrimSpace(c.NodeID) == "" {
			return errors.New("node-id is required in node mode")
		}
	default:
		return fmt.Errorf("unsupported mode %q", c.Mode)
	}

	return nil
}
