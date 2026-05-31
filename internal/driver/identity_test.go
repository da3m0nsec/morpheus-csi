package driver

import (
	"context"
	"log"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/da3m0nsec/morpheus-csi/internal/config"
)

func TestGetPluginInfo(t *testing.T) {
	d := New(config.Config{DriverName: "csi.morpheusdata.com"}, log.Default())

	resp, err := d.GetPluginInfo(context.Background(), &csi.GetPluginInfoRequest{})
	if err != nil {
		t.Fatalf("GetPluginInfo returned error: %v", err)
	}
	if resp.GetName() != "csi.morpheusdata.com" {
		t.Fatalf("expected driver name csi.morpheusdata.com, got %q", resp.GetName())
	}
}

func TestControllerCapabilities(t *testing.T) {
	d := New(config.Default(), log.Default())

	resp, err := d.ControllerGetCapabilities(context.Background(), &csi.ControllerGetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("ControllerGetCapabilities returned error: %v", err)
	}
	if got := len(resp.GetCapabilities()); got != 3 {
		t.Fatalf("expected 3 controller capabilities, got %d", got)
	}
}

func TestNodeCapabilities(t *testing.T) {
	d := New(config.Default(), log.Default())

	resp, err := d.NodeGetCapabilities(context.Background(), &csi.NodeGetCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("NodeGetCapabilities returned error: %v", err)
	}
	if got := len(resp.GetCapabilities()); got != 2 {
		t.Fatalf("expected 2 node capabilities, got %d", got)
	}
}
