package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/da3m0nsec/morpheus-csi/internal/config"
	"github.com/da3m0nsec/morpheus-csi/internal/driver"
)

func main() {
	cfg := config.Default()

	flag.StringVar(&cfg.Endpoint, "endpoint", cfg.Endpoint, "CSI endpoint, for example unix:///csi/csi.sock")
	flag.StringVar(&cfg.DriverName, "driver-name", cfg.DriverName, "CSI driver name")
	flag.StringVar(&cfg.Mode, "mode", cfg.Mode, "driver mode: all, controller, or node")
	flag.StringVar(&cfg.NodeID, "node-id", cfg.NodeID, "CSI node ID")
	flag.StringVar(&cfg.KubeletRootPath, "kubelet-root-path", cfg.KubeletRootPath, "kubelet root path")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level")
	flag.Parse()

	cfg.MorpheusURL = os.Getenv("MORPHEUS_URL")
	cfg.MorpheusToken = os.Getenv("MORPHEUS_TOKEN")
	cfg.MorpheusCAFile = os.Getenv("MORPHEUS_CA_FILE")
	cfg.MorpheusInsecureSkipVerify, _ = strconv.ParseBool(os.Getenv("MORPHEUS_INSECURE_SKIP_VERIFY"))

	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := driver.New(cfg, log.Default())
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("driver stopped with error: %v", err)
	}
}
