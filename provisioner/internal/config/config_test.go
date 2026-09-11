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

	if cfg.StreamKey() != "ctf:request:ops" {
		t.Errorf("StreamKey = %q, want %q", cfg.StreamKey(), "ctf:request:ops")
	}
	if cfg.ResultStreamKey() != "ctf:request:results" {
		t.Errorf("ResultStreamKey = %q, want %q", cfg.ResultStreamKey(), "ctf:request:results")
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

// 에이전트용 Redis를 따로 지정하면 그걸 쓰고, 비워두면 웹과 같은 통으로 떨어져야 한다.
// 폴백이 깨지면 로컬에서 통 하나로 돌릴 때 조용히 빈 주소로 붙으려 든다.
func TestAgentRedisFallsBackToMainRedis(t *testing.T) {
	base := Config{
		RedisAddr:     "nimda-redis:6379",
		RedisPassword: "web-pw",
		RedisDB:       0,
	}

	addr, pw, db := base.AgentRedis()
	if addr != "nimda-redis:6379" || pw != "web-pw" || db != 0 {
		t.Errorf("미지정 시 폴백 실패: addr=%q pw=%q db=%d", addr, pw, db)
	}

	separate := base
	separate.AgentRedisAddr = "ctf-redis:6379"
	separate.AgentRedisPassword = "ctf-pw"
	separate.AgentRedisDB = 3

	addr, pw, db = separate.AgentRedis()
	if addr != "ctf-redis:6379" || pw != "ctf-pw" || db != 3 {
		t.Errorf("지정 시 그 값을 써야 함: addr=%q pw=%q db=%d", addr, pw, db)
	}
}
