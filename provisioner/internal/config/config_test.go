package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.StreamKey() != "ctf:instance:ops" {
		t.Errorf("StreamKey = %q, want %q", cfg.StreamKey(), "ctf:instance:ops")
	}
	if cfg.ConsumerGroup() != "ctf-provisioners" {
		t.Errorf("ConsumerGroup = %q, want %q", cfg.ConsumerGroup(), "ctf-provisioners")
	}
	if cfg.ConsumerName() == "" {
		t.Error("ConsumerName should default to hostname-pid, got empty string")
	}
	if cfg.MaxDeliveries != 5 {
		t.Errorf("MaxDeliveries = %d, want 5", cfg.MaxDeliveries)
	}
	if cfg.PprofAddr != "" {
		t.Errorf("PprofAddr should default to disabled (empty), got %q", cfg.PprofAddr)
	}
}

func TestLoadRespectsEnvOverrides(t *testing.T) {
	t.Setenv("CTF_QUEUE_STREAM_KEY", "test:submissions")
	t.Setenv("CONSUMER_NAME", "worker-under-test")
	t.Setenv("CTF_QUEUE_BLOCK_TIMEOUT", "2s")
	t.Setenv("PPROF_ADDR", "localhost:6060")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.StreamKey() != "test:submissions" {
		t.Errorf("StreamKey = %q, want override value", cfg.StreamKey())
	}
	if cfg.ConsumerName() != "worker-under-test" {
		t.Errorf("ConsumerName = %q, want override value", cfg.ConsumerName())
	}
	if cfg.BlockTimeout != 2*time.Second {
		t.Errorf("BlockTimeout = %v, want 2s", cfg.BlockTimeout)
	}
	if cfg.PprofAddr != "localhost:6060" {
		t.Errorf("PprofAddr = %q, want override value", cfg.PprofAddr)
	}
}

func TestLoadRejectsInvalidMaxDeliveries(t *testing.T) {
	t.Setenv("CTF_QUEUE_MAX_DELIVERIES", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for CTF_QUEUE_MAX_DELIVERIES=0, got none")
	}
	if !strings.Contains(err.Error(), "CTF_QUEUE_MAX_DELIVERIES") {
		t.Errorf("error message %q does not mention the offending key", err.Error())
	}
}
