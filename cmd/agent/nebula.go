package main

import (
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/service"
)

// currentNebula is the running embedded Nebula instance.
// Protected by nebulaMu for concurrent access from main and renewal goroutines.
var (
	currentNebula *nebulaService
	nebulaMu      sync.Mutex
)

// onNebulaRestart is called after Nebula is successfully restarted (cert renewal).
// Set by runServe() to recreate the mesh HTTP listener.
var onNebulaRestart func(svc *nebulaService)

// nebulaService wraps an embedded Nebula userspace instance.
type nebulaService struct {
	svc *service.Service
}

// startNebula starts an embedded Nebula instance from a config file.
// The agent joins the mesh as a regular node (not lighthouse).
func startNebula(configPath string) (*nebulaService, error) {
	var cfg config.C
	if err := cfg.Load(configPath); err != nil {
		return nil, fmt.Errorf("load nebula config %s: %w", configPath, err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	ctrl, err := nebula.Main(&cfg, false, "hop-agent", logger, overlay.NewUserDeviceFromConfig)
	if err != nil {
		return nil, fmt.Errorf("start nebula: %w", err)
	}

	svc, err := service.New(ctrl)
	if err != nil {
		return nil, fmt.Errorf("create nebula service: %w", err)
	}

	return &nebulaService{svc: svc}, nil
}

// Listen creates a TCP listener on the Nebula mesh's userspace network stack.
// Connections arriving through the mesh tunnel are accepted by this listener.
func (n *nebulaService) Listen(network, address string) (net.Listener, error) {
	return n.svc.Listen(network, address)
}

// Close shuts down the Nebula instance gracefully.
func (n *nebulaService) Close() {
	log.Printf("[agent] stopping Nebula mesh connection")
	n.svc.Close()
}
