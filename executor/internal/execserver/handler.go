package execserver

import (
	"fmt"
	"os"
	"path/filepath"

	"nimda-judge-executor/internal/isolate"
	"nimda-judge-executor/internal/language"
	"nimda-judge-protocol"
)

// Handler owns the box pool and dispatches one HandleConn goroutine per
// incoming connection. MetaDir is where isolate writes its per-run meta
// files (outside the box, on the Executor's local disk).
type Handler struct {
	Pool    *isolate.BoxPool
	MetaDir string
}

// session holds the state of one Start..End lifecycle: the box acquired for
// this submission and the language it was compiled with (needed by Run to
// know the run command, process limit, env, etc.).
type session struct {
	box  *isolate.Box
	lang language.Language
}

// handleFrame processes exactly one RequestFrame against the current
// session (nil until Start succeeds) and returns the response to send back,
// plus the possibly-updated session and whether the connection should close
// (End, or an unrecoverable error).
func (h *Handler) handleFrame(sess *session, req protocol.RequestFrame) (protocol.ResponseFrame, *session, bool) {
	switch req.Type {
	case protocol.FrameStart:
		if req.Start == nil {
			return errorFrame(protocol.FrameStart, "start 요청에 payload가 없습니다"), sess, true
		}
		newSess, resp, err := h.start(req.Start)
		if err != nil {
			return errorFrame(protocol.FrameStart, err.Error()), sess, true
		}
		return protocol.ResponseFrame{Type: protocol.FrameStart, Start: resp}, newSess, false

	case protocol.FrameRun:
		if sess == nil {
			return errorFrame(protocol.FrameRun, "start 없이 run을 호출했습니다"), sess, true
		}
		if req.Run == nil {
			return errorFrame(protocol.FrameRun, "run 요청에 payload가 없습니다"), sess, true
		}
		resp, err := h.run(sess, req.Run)
		if err != nil {
			return errorFrame(protocol.FrameRun, err.Error()), sess, true
		}
		return protocol.ResponseFrame{Type: protocol.FrameRun, Run: resp}, sess, false

	case protocol.FrameEnd:
		if sess != nil {
			_ = h.Pool.Release(sess.box)
		}
		return protocol.ResponseFrame{Type: protocol.FrameEnd}, nil, true

	default:
		return errorFrame(req.Type, fmt.Sprintf("알 수 없는 frame type: %s", req.Type)), sess, true
	}
}

func errorFrame(t protocol.FrameType, msg string) protocol.ResponseFrame {
	return protocol.ResponseFrame{Type: t, Error: msg}
}

// start acquires a box, writes the source, and compiles it (if the language
// needs compiling). The box stays open (not released) so Run can reuse it
// for every test case - only End (or an error here) releases it.
func (h *Handler) start(req *protocol.StartRequest) (*session, *protocol.StartResponse, error) {
	lang, err := language.GetLanguage(req.Language)
	if err != nil {
		return nil, nil, err
	}

	box, err := h.Pool.Acquire(req.UseCgroups)
	if err != nil {
		return nil, nil, fmt.Errorf("샌드박스 획득 실패: %w", err)
	}
	sess := &session{box: box, lang: lang}

	if err := box.WriteFile(lang.SourceFileName, req.SourceCode, 0644); err != nil {
		_ = h.Pool.Release(box)
		return nil, nil, fmt.Errorf("소스코드 기록 실패: %w", err)
	}

	if lang.CompileCmd == nil {
		return sess, &protocol.StartResponse{CompileOK: true}, nil
	}

	metaPath := filepath.Join(h.MetaDir, fmt.Sprintf("compile-%d.meta", box.ID))
	stderrRel := "compile_stderr.txt"

	timeLimit := lang.CompileTimeLimitSec
	if timeLimit <= 0 {
		timeLimit = 10
	}
	memLimit := lang.CompileMemoryLimitKB
	if memLimit <= 0 {
		memLimit = 512 * 1024
	}

	cfg := isolate.RunConfig{
		Command:          lang.CompileCmd(),
		TimeLimitSec:     timeLimit,
		WallTimeLimitSec: timeLimit * 2,
		MemoryLimitKB:    memLimit,
		ProcessLimit:     16, // 컴파일러는 내부적으로 서브프로세스를 띄우는 경우가 많음 (cc1, ld 등)
		StderrPath:       stderrRel,
		MetaPath:         metaPath,
		FullEnv:          true, // 컴파일러가 자기 표준 경로들을 찾을 수 있게
		Env: map[string]string{
			"GOCACHE": "/box/.gocache",
			"HOME":    "/box",
		},
	}

	res, err := box.Run(cfg)
	if err != nil {
		_ = h.Pool.Release(box)
		return nil, nil, fmt.Errorf("컴파일 실행 실패: %w", err)
	}
	defer os.Remove(metaPath)

	stderrContent, _ := box.ReadFile(stderrRel)

	if res.Status != isolate.StatusOK || res.ExitCode != 0 {
		return sess, &protocol.StartResponse{CompileOK: false, CompileMessage: string(stderrContent)}, nil
	}
	return sess, &protocol.StartResponse{CompileOK: true, CompileMessage: string(stderrContent)}, nil
}

// run executes the compiled program once against req.Stdin, inside the box
// opened by start.
func (h *Handler) run(sess *session, req *protocol.RunRequest) (*protocol.RunResponse, error) {
	stdinRel := "input.txt"
	stdoutRel := "output.txt"
	stderrRel := "run_stderr.txt"
	metaPath := filepath.Join(h.MetaDir, fmt.Sprintf("run-%d-%d.meta", sess.box.ID, os.Getpid()))
	defer os.Remove(metaPath)

	if err := sess.box.WriteFile(stdinRel, req.Stdin, 0644); err != nil {
		return nil, fmt.Errorf("입력 파일 기록 실패: %w", err)
	}

	// Go/JVM처럼 시작시 가상주소공간을 크게 선점하는 언어는, cgroup 없이 --mem(RLIMIT_AS)을
	// 걸면 실제 메모리를 쓰기도 전에 즉시 죽어버린다. 이 경우 --mem 자체를 생략하고,
	// 실행 후 실측 max-rss로만 클라이언트가 MLE를 판정하게 한다.
	memLimitKB := req.MemoryLimitKB
	if sess.lang.SkipVirtualMemLimit && !sess.box.UseCgroups {
		memLimitKB = 0
	}

	processLimit := sess.lang.RunProcessLimit
	if processLimit <= 0 {
		processLimit = 1
	}

	cfg := isolate.RunConfig{
		Command:          sess.lang.RunCmd(),
		TimeLimitSec:     req.TimeLimitSec,
		WallTimeLimitSec: req.TimeLimitSec*2 + 1, // 무한 sleep 등으로 CPU time은 안 쓰면서 시간만 끄는 경우 방지
		ExtraTimeSec:     0.5,                    // TLE 경계에서 오차로 인한 오탐 방지용 유예
		MemoryLimitKB:    memLimitKB,
		ProcessLimit:     processLimit,
		Env:              sess.lang.RunEnv,
		FileSizeLimitKB:  req.OutputLimitKB,
		StdinPath:        stdinRel,
		StdoutPath:       stdoutRel,
		StderrPath:       stderrRel,
		MetaPath:         metaPath,
	}

	res, err := sess.box.Run(cfg)
	if err != nil {
		return nil, err
	}

	stdout, readErr := sess.box.ReadFile(stdoutRel)
	if readErr != nil {
		stdout = []byte{}
	}

	return &protocol.RunResponse{
		Status:    string(res.Status),
		ExitCode:  res.ExitCode,
		ExitSig:   res.ExitSig,
		TimeSec:   res.TimeSec,
		WallSec:   res.WallSec,
		MaxRSSKB:  res.MaxRSSKB,
		Message:   res.Message,
		CgOOMKill: res.CgOOMKill,
		Stdout:    stdout,
	}, nil
}
