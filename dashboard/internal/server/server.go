// 인스턴스 모니터링 대시보드 (Feature-01).
//
// agent가 ctf-redis에 써둔 snapshot을 읽어서 보여주기만 한다.
// 컨테이너를 만지지 않고, 웹사이트와도 통신하지 않는다.
//
// 인증은 없다. 대신 호스트의 127.0.0.1에만 열고 SSH 터널로 접근한다.
// 그래서 대시보드에 닿을 수 있는 사람은 SSH 키를 가진 사람뿐이다.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/redis/go-redis/v9"

	protocol "nimda-ctf-protocol"
)

//go:embed static/index.html
var static embed.FS

// snapshot을 읽어올 곳. 테스트에서 가짜로 바꾼다.
type SnapshotReader interface {
	Get(ctx context.Context, key string) *redis.StringCmd
}

type Server struct {
	redis        SnapshotReader
	allowedHosts map[string]bool
	log          *slog.Logger
}

// allowedHosts는 Host 헤더로 허용할 호스트 이름이다(포트 제외).
func New(r SnapshotReader, allowedHosts []string, log *slog.Logger) *Server {
	hosts := make(map[string]bool, len(allowedHosts))
	for _, h := range allowedHosts {
		if h = strings.TrimSpace(strings.ToLower(h)); h != "" {
			hosts[h] = true
		}
	}
	return &Server{redis: r, allowedHosts: hosts, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/instances", s.instances)
	return s.guard(mux)
}

// 모든 요청에 거는 검사.
//
// Host 헤더를 보는 이유는 DNS rebinding 때문이다.
// SSH 터널을 열어둔 채로 브라우저에서 악성 사이트에 들어갔다고 가정하자.
// 그 사이트가 자기 도메인을 127.0.0.1로 바꾸면, 브라우저는 같은 출처라고 보고 localhost:6070을 읽게 해준다.
// 이때 Host 헤더에는 그 사이트의 도메인이 들어오기 때문에 여기서 막을 수 있다.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !s.allowedHosts[strings.ToLower(host)] {
			s.log.Warn("허용되지 않은 Host로 접근", "host", r.Host, "remote", r.RemoteAddr)
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}

		h := w.Header()
		// 화면은 자기 자신 말고는 아무것도 불러오지 않는다.
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	b, err := static.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// snapshot을 그대로 내려준다.
// 키가 없으면 agent가 30초 넘게 갱신하지 않은 것이라 503을 준다.
func (s *Server) instances(w http.ResponseWriter, r *http.Request) {
	b, err := s.redis.Get(r.Context(), protocol.MonitorSnapshotKey).Bytes()
	if errors.Is(err, redis.Nil) {
		writeError(w, http.StatusServiceUnavailable, "agent 상태 정보를 받을 수 없습니다")
		return
	}
	if err != nil {
		s.log.Error("snapshot 조회 실패", "error", err)
		writeError(w, http.StatusBadGateway, "ctf-redis에 연결할 수 없습니다")
		return
	}
	// agent가 쓴 값이 JSON이 아니면 그대로 내보내지 않는다.
	if !json.Valid(b) {
		s.log.Error("snapshot이 JSON이 아님")
		writeError(w, http.StatusBadGateway, "snapshot 형식이 잘못됐습니다")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
