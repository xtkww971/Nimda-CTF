package agentserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"

	protocol "nimda-ctf-protocol"
)

// 인스턴스 생성 및 파괴, 상태 조회의 기능을 담당
type Handler interface {
	Create(ctx context.Context, req *protocol.CreateRequest, buildContext []byte) *protocol.CreateResult
	Destroy(ctx context.Context, req *protocol.DestroyRequest) *protocol.DestroyResult
	Status(ctx context.Context, req *protocol.StatusRequest) *protocol.StatusResult
	List(ctx context.Context, req *protocol.ListRequest) *protocol.ListResult
}

// UPS 통신 서버
func Serve(ctx context.Context, ln net.Listener, h Handler, log *slog.Logger) {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return // shutting down
			}
			log.Warn("accept 실패", "error", err)
			continue
		}
		go handleConn(ctx, conn, h, log)
	}
}

func handleConn(ctx context.Context, conn net.Conn, h Handler, log *slog.Logger) {
	defer conn.Close()

	var req protocol.RequestFrame
	if err := protocol.ReadFrame(conn, &req); err != nil {
		if !errors.Is(err, io.EOF) {
			log.Warn("요청 프레임 읽기 실패", "error", err)
		}
		return
	}

	// 채널로 들어온 요청 처리
	switch req.Type {
	case protocol.TypeCreate:
		handleCreate(ctx, conn, &req, h, log)
	case protocol.TypeDestroy:
		handleDestroy(ctx, conn, &req, h, log)
	case protocol.TypeStatus:
		handleStatus(ctx, conn, &req, h, log)
	case protocol.TypeList:
		handleList(ctx, conn, &req, h, log)
	default:
		writeError(conn, req.Type, "알 수 없는 요청 타입", log)
	}
}

func handleList(ctx context.Context, conn net.Conn, req *protocol.RequestFrame, h Handler, log *slog.Logger) {
	listReq := req.List
	if listReq == nil {
		listReq = &protocol.ListRequest{}
	}
	result := h.List(ctx, listReq)
	writeResponse(conn, protocol.ResponseFrame{Type: protocol.TypeList, List: result}, log)
}

func handleCreate(ctx context.Context, conn net.Conn, req *protocol.RequestFrame, h Handler, log *slog.Logger) {
	if req.Create == nil {
		writeError(conn, protocol.TypeCreate, "create payload 누락", log)
		return
	}
	// The build context follows the request as its own framed blob.
	buildContext, err := protocol.ReadBlob(conn)
	if err != nil {
		writeError(conn, protocol.TypeCreate, "빌드 컨텍스트 읽기 실패: "+err.Error(), log)
		return
	}

	result := h.Create(ctx, req.Create, buildContext)
	if result == nil {
		writeError(conn, protocol.TypeCreate, "핸들러가 결과를 반환하지 않음", log)
		return
	}
	writeResponse(conn, protocol.ResponseFrame{Type: protocol.TypeCreate, Create: result}, log)
}

func handleDestroy(ctx context.Context, conn net.Conn, req *protocol.RequestFrame, h Handler, log *slog.Logger) {
	if req.Destroy == nil {
		writeError(conn, protocol.TypeDestroy, "destroy payload 누락", log)
		return
	}
	result := h.Destroy(ctx, req.Destroy)
	writeResponse(conn, protocol.ResponseFrame{Type: protocol.TypeDestroy, Destroy: result}, log)
}

func handleStatus(ctx context.Context, conn net.Conn, req *protocol.RequestFrame, h Handler, log *slog.Logger) {
	if req.Status == nil {
		writeError(conn, protocol.TypeStatus, "status payload 누락", log)
		return
	}
	result := h.Status(ctx, req.Status)
	writeResponse(conn, protocol.ResponseFrame{Type: protocol.TypeStatus, Status: result}, log)
}

func writeResponse(conn net.Conn, resp protocol.ResponseFrame, log *slog.Logger) {
	if err := protocol.WriteFrame(conn, resp); err != nil {
		log.Warn("응답 프레임 쓰기 실패", "error", err)
	}
}

func writeError(conn net.Conn, t protocol.Type, msg string, log *slog.Logger) {
	writeResponse(conn, protocol.ResponseFrame{Type: t, Error: msg}, log)
}
