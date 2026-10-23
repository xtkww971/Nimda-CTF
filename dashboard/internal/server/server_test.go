package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// Redis 대신 정해진 값을 돌려준다.
type fakeRedis struct {
	val string
	err error
}

func (f fakeRedis) Get(ctx context.Context, _ string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	if f.err != nil {
		cmd.SetErr(f.err)
	} else {
		cmd.SetVal(f.val)
	}
	return cmd
}

func newServer(r fakeRedis) http.Handler {
	return New(r, []string{"localhost", "127.0.0.1"}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}

func get(h http.Handler, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestInstancesReturnsSnapshot(t *testing.T) {
	rec := get(newServer(fakeRedis{val: `{"summary":{"containers":1}}`}), "localhost:6070", "/api/instances")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"containers":1`) {
		t.Errorf("body = %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
}

// 키가 없으면 agent가 갱신을 멈춘 것이다. 화면이 "agent 응답 없음"을 띄울 수 있게 503.
func TestInstancesWithoutSnapshotIs503(t *testing.T) {
	rec := get(newServer(fakeRedis{err: redis.Nil}), "localhost", "/api/instances")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestInstancesRedisDownIs502(t *testing.T) {
	rec := get(newServer(fakeRedis{err: errors.New("dial tcp: refused")}), "localhost", "/api/instances")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "refused") {
		t.Error("내부 에러 메시지가 응답에 노출됨")
	}
}

func TestInstancesRejectsNonJSON(t *testing.T) {
	rec := get(newServer(fakeRedis{val: "<script>"}), "localhost", "/api/instances")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

// DNS rebinding: 다른 도메인이 127.0.0.1로 풀려서 들어와도 Host 헤더로 막는다.
func TestRejectsForeignHost(t *testing.T) {
	h := newServer(fakeRedis{val: `{}`})
	for _, host := range []string{"evil.example.com", "evil.example.com:6070", "ctf-dashboard:6070"} {
		if rec := get(h, host, "/api/instances"); rec.Code != http.StatusForbidden {
			t.Errorf("host=%s status = %d, want 403", host, rec.Code)
		}
	}
	for _, host := range []string{"localhost", "localhost:6070", "127.0.0.1:6070", "LOCALHOST:6070"} {
		if rec := get(h, host, "/api/instances"); rec.Code != http.StatusOK {
			t.Errorf("host=%s status = %d, want 200", host, rec.Code)
		}
	}
}

func TestIndexServesHTMLWithSecurityHeaders(t *testing.T) {
	rec := get(newServer(fakeRedis{}), "localhost", "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CTF 인스턴스 모니터링") {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, hdr := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if rec.Header().Get(hdr) == "" {
			t.Errorf("%s 헤더 없음", hdr)
		}
	}
}

func TestUnknownPathAndMethod(t *testing.T) {
	h := newServer(fakeRedis{val: `{}`})
	if rec := get(h, "localhost", "/admin"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/instances", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
