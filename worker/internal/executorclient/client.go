// Package executorclient is the Orchestrator side of the ADR-0005 unix
// socket protocol - see nimda-judge-protocol for the shared wire types and
// executor/internal/execserver for the Executor side.
package executorclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"nimda-judge-protocol"
)

// Client dials the Executor's unix socket. It holds no state beyond the
// socket path - each judged submission opens its own Session (see
// judge.go's JudgeSubmission), so Sessions never need to be shared or
// pooled across goroutines.
type Client struct {
	SocketPath string
}

func New(socketPath string) *Client {
	return &Client{SocketPath: socketPath}
}

// Session is one Start -> Run* -> End lifecycle over a single connection,
// matching the Executor's per-connection session in execserver.
type Session struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

// NewSession dials the Executor. The connection is not usable until Start
// succeeds; callers must call End (even after a Start/Run error) to release
// the underlying box on the Executor side.
func (c *Client) NewSession(ctx context.Context) (*Session, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("executor 소켓(%s) 연결 실패: %w", c.SocketPath, err)
	}
	return &Session{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}, nil
}

func (s *Session) Start(req protocol.StartRequest) (*protocol.StartResponse, error) {
	resp, err := s.roundTrip(protocol.RequestFrame{Type: protocol.FrameStart, Start: &req})
	if err != nil {
		return nil, err
	}
	if resp.Start == nil {
		return nil, errors.New("executor가 start 응답을 비워서 보냈습니다")
	}
	return resp.Start, nil
}

func (s *Session) Run(req protocol.RunRequest) (*protocol.RunResponse, error) {
	resp, err := s.roundTrip(protocol.RequestFrame{Type: protocol.FrameRun, Run: &req})
	if err != nil {
		return nil, err
	}
	if resp.Run == nil {
		return nil, errors.New("executor가 run 응답을 비워서 보냈습니다")
	}
	return resp.Run, nil
}

// End tells the Executor to clean up the box and closes the connection.
// Safe to call multiple times; call it via defer right after NewSession
// succeeds so a mid-session error still releases the box.
func (s *Session) End() error {
	defer s.conn.Close()
	_, err := s.roundTrip(protocol.RequestFrame{Type: protocol.FrameEnd})
	return err
}

func (s *Session) roundTrip(req protocol.RequestFrame) (*protocol.ResponseFrame, error) {
	if err := s.enc.Encode(req); err != nil {
		return nil, fmt.Errorf("executor 요청 전송 실패: %w", err)
	}
	var resp protocol.ResponseFrame
	if err := s.dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("executor 응답 수신 실패: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("executor 오류: %s", resp.Error)
	}
	return &resp, nil
}
