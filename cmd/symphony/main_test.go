package main

import (
	"testing"

	"github.com/tomi/my-symphony/internal/config"
)

func TestResolvePort(t *testing.T) {
	// CLI overrides server.port.
	cfg := &config.Config{}
	cfg.Server = config.ServerConfig{Port: 8080, PortSet: true}
	if p, enabled := resolvePort(9090, cfg); !enabled || p != 9090 {
		t.Errorf("CLI port should win: got %d enabled=%v", p, enabled)
	}
	// server.port used when no CLI port.
	if p, enabled := resolvePort(-1, cfg); !enabled || p != 8080 {
		t.Errorf("server.port should apply: got %d enabled=%v", p, enabled)
	}
	// Disabled when neither.
	cfg.Server = config.ServerConfig{}
	if _, enabled := resolvePort(-1, cfg); enabled {
		t.Errorf("http should be disabled when no port configured")
	}
}
