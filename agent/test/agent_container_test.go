package test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"nimda-ctf-agent/internal/agentserver"
	"nimda-ctf-agent/internal/runtime"
	"nimda-ctf-protocol"
)

const accessMarker = "CTF_AGENT_ACCESS_OK"

// TestAgentBuildsRunsAndIsReachable drives the whole Agent Create path against
// real Docker: a client (playing the Provisioner) sends a build context, the
// Agent builds an image, runs a container, and returns host:port. The test
// then HTTP-GETs that address to prove the instance is actually reachable, and
// finishes with Status (RUNNING) and Destroy.
func TestAgentBuildsRunsAndIsReachable(t *testing.T) {
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker를 사용할 수 없어 건너뜁니다: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := runtime.NewDocker(runtime.Options{MemoryLimit: "128m", PidsLimit: 128})
	handler := agentserver.NewDockerHandler(rt, "127.0.0.1", logger)

	// Production uses a unix socket; the test uses TCP on loopback so it also
	// runs on Windows (unix socket paths hit length limits under temp dirs).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agentserver.Serve(ctx, ln, handler, logger)

	addr := ln.Addr().String()

	// A unique code -> unique image/container name, so runs don't collide.
	code := fmt.Sprintf("agenttest%d", time.Now().Unix())
	image := "ctf-chal-" + code + ":latest"
	t.Cleanup(func() {
		bg := context.Background()
		_ = rt.Remove(bg, "ctf-"+code+"-u7") // PER_USER name
		rt.RemoveImage(bg, image)
	})

	// ── Create ──────────────────────────────────────────────────────────
	buildCtx := makeBusyboxBuildContext(t)
	createResp := doCreate(t, addr, &protocol.CreateRequest{
		RequestID:     "req-create-1",
		ChallengeCode: code,
		UserID:        7,
		IsolationType: "PER_USER",
		FlagType:      "STATIC",
	}, buildCtx)

	if createResp.Error != "" || createResp.Create == nil {
		t.Fatalf("create가 에러 반환: %q / %+v", createResp.Error, createResp.Create)
	}
	res := createResp.Create
	if res.Status != protocol.StatusReady {
		t.Fatalf("status = %q, want READY (message=%q)", res.Status, res.Message)
	}
	if res.Host != "127.0.0.1" || res.Port == 0 || res.InstanceID == "" {
		t.Fatalf("접근 정보가 비어있음: %+v", res)
	}
	if res.ExpiresAt == "" {
		t.Errorf("PER_USER 인스턴스는 만료 시각이 있어야 함")
	}
	t.Logf("인스턴스 생성됨: %s:%d (id=%s)", res.Host, res.Port, res.InstanceID)

	// ── 접근 확인 (핵심) ────────────────────────────────────────────────
	url := fmt.Sprintf("http://%s:%d/", res.Host, res.Port)
	body := getWithRetry(t, url, 15*time.Second)
	if !bytes.Contains(body, []byte(accessMarker)) {
		t.Fatalf("접근은 됐지만 응답에 마커가 없음: %q", string(body))
	}
	t.Logf("접근 확인 완료: %s -> 마커 응답", url)

	// ── List (실시간 포트 조회) ─────────────────────────────────────────
	listResp := doSimple(t, addr, protocol.RequestFrame{
		Type: protocol.TypeList,
		List: &protocol.ListRequest{RequestID: "req-list-1"},
	})
	if listResp.List == nil {
		t.Fatalf("list 응답 없음: %+v (err=%q)", listResp, listResp.Error)
	}
	foundInList := false
	for _, in := range listResp.List.Instances {
		if in.Port == res.Port && in.ChallengeCode == code {
			foundInList = true
		}
	}
	if !foundInList {
		t.Errorf("list에 방금 만든 인스턴스(port=%d, code=%s)가 없음: %+v", res.Port, code, listResp.List.Instances)
	}

	// ── Status ──────────────────────────────────────────────────────────
	statusResp := doSimple(t, addr, protocol.RequestFrame{
		Type:   protocol.TypeStatus,
		Status: &protocol.StatusRequest{RequestID: "req-status-1", InstanceID: res.InstanceID},
	})
	if statusResp.Status == nil || statusResp.Status.State != protocol.StateRunning {
		t.Fatalf("status가 RUNNING이 아님: %+v (err=%q)", statusResp.Status, statusResp.Error)
	}

	// ── Destroy ─────────────────────────────────────────────────────────
	destroyResp := doSimple(t, addr, protocol.RequestFrame{
		Type:    protocol.TypeDestroy,
		Destroy: &protocol.DestroyRequest{RequestID: "req-destroy-1", InstanceID: res.InstanceID},
	})
	if destroyResp.Destroy == nil || destroyResp.Destroy.Status != protocol.DestroyDone {
		t.Fatalf("destroy 실패: %+v (err=%q)", destroyResp.Destroy, destroyResp.Error)
	}

	// 삭제 후에는 접근이 안 돼야 한다.
	if _, err := http.Get(url); err == nil {
		t.Errorf("삭제 후에도 %s 에 접근됨", url)
	}
}

// ── client helpers (Provisioner 역할) ──────────────────────────────────────

func doCreate(t *testing.T, addr string, req *protocol.CreateRequest, buildCtx []byte) protocol.ResponseFrame {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := protocol.WriteFrame(conn, protocol.RequestFrame{Type: protocol.TypeCreate, Create: req}); err != nil {
		t.Fatalf("write create frame: %v", err)
	}
	if err := protocol.WriteBlob(conn, buildCtx); err != nil {
		t.Fatalf("write build context: %v", err)
	}
	var resp protocol.ResponseFrame
	if err := protocol.ReadFrame(conn, &resp); err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp
}

func doSimple(t *testing.T, addr string, req protocol.RequestFrame) protocol.ResponseFrame {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := protocol.WriteFrame(conn, req); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	var resp protocol.ResponseFrame
	if err := protocol.ReadFrame(conn, &resp); err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp
}

// makeBusyboxBuildContext builds a tar with a Dockerfile + index.html under a
// "chal/" subfolder (mirroring the real challenge zip, which wraps its files),
// so we also exercise the Agent finding a nested Dockerfile.
func makeBusyboxBuildContext(t *testing.T) []byte {
	t.Helper()
	dockerfile := "FROM busybox\n" +
		"COPY index.html /www/index.html\n" +
		"EXPOSE 8080\n" +
		"CMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/www\"]\n"
	indexHTML := "<html><body>" + accessMarker + "</body></html>\n"

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTarFile(t, tw, "chal/Dockerfile", dockerfile)
	writeTarFile(t, tw, "chal/index.html", indexHTML)
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

func writeTarFile(t *testing.T, tw *tar.Writer, name, content string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar header %s: %v", name, err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("tar write %s: %v", name, err)
	}
}

func getWithRetry(t *testing.T, url string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return body
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s 접근 실패 (%s 내): %v", url, timeout, lastErr)
	return nil
}
