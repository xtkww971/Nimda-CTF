// Command smoketest exercises the ADR-0005 Orchestrator<->Executor unix
// socket protocol end-to-end, independent of Redis/S3/the queue pipeline:
// it dials EXECUTOR_SOCKET_PATH, runs a trivial python3 submission through
// a full Start->Run->End session, and prints what came back. Exists purely
// to verify the two containers can actually talk to each other; it is not
// part of the judging pipeline itself.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"nimda-judge-worker/internal/executorclient"

	"nimda-judge-protocol"
)

func main() {
	socketPath := os.Getenv("EXECUTOR_SOCKET_PATH")
	if socketPath == "" {
		socketPath = "/var/run/judge/executor.sock"
	}

	fmt.Printf("[smoketest] connecting to executor at %s\n", socketPath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := executorclient.New(socketPath)
	sess, err := client.NewSession(ctx)
	if err != nil {
		fail("세션 연결 실패", err)
	}
	defer func() { _ = sess.End() }()

	fmt.Println("[smoketest] connected. sending Start (python3)")
	startResp, err := sess.Start(protocol.StartRequest{
		Language:   "python3",
		SourceCode: []byte("print('hello from executor')\n"),
	})
	if err != nil {
		fail("Start 요청 실패", err)
	}
	if !startResp.CompileOK {
		fail("컴파일 실패", fmt.Errorf("%s", startResp.CompileMessage))
	}
	fmt.Println("[smoketest] Start OK (컴파일 불필요한 언어이거나 컴파일 성공)")

	fmt.Println("[smoketest] sending Run")
	runResp, err := sess.Run(protocol.RunRequest{
		Stdin:         nil,
		TimeLimitSec:  5,
		MemoryLimitKB: 256 * 1024,
		OutputLimitKB: 1024,
	})
	if err != nil {
		fail("Run 요청 실패", err)
	}

	fmt.Printf("[smoketest] Run 결과: status=%s exitCode=%d timeSec=%.3f maxRssKb=%d\n",
		runResp.Status, runResp.ExitCode, runResp.TimeSec, runResp.MaxRSSKB)
	fmt.Printf("[smoketest] stdout: %q\n", string(runResp.Stdout))

	if runResp.Status != "OK" || string(runResp.Stdout) != "hello from executor\n" {
		fail("예상과 다른 결과", fmt.Errorf("status=%s stdout=%q", runResp.Status, runResp.Stdout))
	}

	fmt.Println("[smoketest] PASS: executor와 정상적으로 통신했고 실행 결과도 기대값과 일치합니다")
}

func fail(msg string, err error) {
	fmt.Fprintf(os.Stderr, "[smoketest] FAIL: %s: %v\n", msg, err)
	os.Exit(1)
}
