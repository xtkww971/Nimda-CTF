package execserver

import (
	"encoding/json"
	"log"
	"net"

	"nimda-judge-protocol"
)

// Serve accepts connections on ln forever, handling each one in its own
// goroutine. It returns only when ln.Accept fails (e.g. the listener was
// closed by the caller during shutdown).
func Serve(ln net.Listener, h *Handler) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleConn(conn, h)
	}
}

// handleConn runs one Start->Run*->End session for a single connection.
// json.Decoder.Decode reads consecutive JSON values off the stream, so no
// length-prefix framing is needed - the two sides simply agree on the
// Start/Run/End sequence.
func handleConn(conn net.Conn, h *Handler) {
	defer conn.Close()

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var sess *session
	defer func() {
		if sess != nil {
			_ = h.Pool.Release(sess.box)
		}
	}()

	for {
		var req protocol.RequestFrame
		if err := dec.Decode(&req); err != nil {
			return // EOF or malformed stream - connection is done either way
		}

		resp, newSess, done := h.handleFrame(sess, req)
		sess = newSess

		if err := enc.Encode(resp); err != nil {
			log.Printf("executor: 응답 인코딩 실패, 연결 종료: %v", err)
			return
		}
		if done {
			return
		}
	}
}
