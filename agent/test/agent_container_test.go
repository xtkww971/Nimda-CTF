package test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"nimda-ctf-agent/internal/agentserver"
	"nimda-ctf-agent/internal/runtime"
	protocol "nimda-ctf-protocol"
)

const accessMarker = "CTF_AGENT_ACCESS_OK"

// TestAgentBuildsRunsAndIsReachable drives the whole Agent Create path against
// real Docker: given a build context, the Agent builds an image, runs a
// container, and returns host:port. The test then HTTP-GETs that address to
// prove the instance is actually reachable, and finishes with Status (RUNNING)
// and Destroy.
//
// 떠 있는 컨테이너 목록을 통신으로 주고받던 경로는 걷어냈다. 어느 요청이 어느
// 컨테이너인지는 조율자·에이전트가 공유하는 redis의 uuid: 기록이 들고 있다.
//
// 소켓 시절엔 이 테스트가 TCP로 프레임을 주고받으며 조율자 흉내를 냈다. 전송 계층을
// 걷어냈으므로(ADR-0012) 이제 핸들러를 직접 부른다. 여기서 확인하려는 건 애초에
// 전송이 아니라 "진짜 도커로 빌드해서 띄운 게 접속되는가"였다.
func TestAgentBuildsRunsAndIsReachable(t *testing.T) {
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skipf("docker를 사용할 수 없어 건너뜁니다: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := runtime.NewDocker(runtime.Options{MemoryLimit: "128m", PidsLimit: 128})
	if err != nil {
		t.Skipf("도커 데몬에 연결하지 못해 건너뜁니다: %v", err)
	}
	defer rt.Close()

	// 조율자가 압축을 푸는 공유 폴더 역할. Agent는 이 루트 아래 경로만 빌드한다.
	buildRoot := t.TempDir()
	handler := agentserver.NewDockerHandler(rt, "127.0.0.1", buildRoot, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A unique code -> unique image/container name, so runs don't collide.
	code := fmt.Sprintf("agenttest%d", time.Now().Unix())
	image := "ctf-chal-" + code + ":latest"
	// 웹이 사용자·문제마다 발급하는 값 자리. PER_USER 컨테이너 이름의 구분자가 된다.
	uuid := fmt.Sprintf("uuid-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		bg := context.Background()
		_ = rt.Remove(bg, "ctf-"+code+"-"+uuid) // PER_USER name
		rt.RemoveImage(bg, image)
	})

	// ── Create ──────────────────────────────────────────────────────────
	// 조율자가 한 것처럼, 빌드 파일을 공유 폴더에 풀어두고 그 경로만 넘긴다.
	buildDir := writeBusyboxBuildDir(t, buildRoot)
	res := handler.Create(ctx, &protocol.RequestMessage{
		UUID:             uuid,
		Action:           protocol.ActionCreate,
		ChallengeCode:    code,
		IsolationType:    "PER_USER",
		FlagType:         "STATIC",
		BuildContextPath: buildDir,
	})

	if res == nil {
		t.Fatal("create가 결과를 반환하지 않음")
	}
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

	// ── Status ──────────────────────────────────────────────────────────
	status := handler.Status(ctx, &protocol.StatusRequest{UUID: uuid, InstanceID: res.InstanceID})
	if status == nil || status.State != protocol.StateRunning {
		t.Fatalf("status가 RUNNING이 아님: %+v", status)
	}

	// ── Destroy ─────────────────────────────────────────────────────────
	destroy := handler.Destroy(ctx, uuid, res.InstanceID)
	if destroy == nil || destroy.Status != protocol.StatusDestroyed {
		t.Fatalf("destroy 실패: %+v", destroy)
	}

	// 삭제 후에는 접근이 안 돼야 한다.
	if _, err := http.Get(url); err == nil {
		t.Errorf("삭제 후에도 %s 에 접근됨", url)
	}
}

// 빌드 컨텍스트가 허용된 루트 밖이면 거부해야 한다. 조율자가 뚫려도 임의 호스트 경로를
// 빌드 컨텍스트로 삼지 못하게 막는 경계라, 도커 없이도 확인해 둔다.
func TestAgentRejectsBuildPathOutsideRoot(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := runtime.NewDocker(runtime.Options{})
	if err != nil {
		t.Skipf("도커 데몬에 연결하지 못해 건너뜁니다: %v", err)
	}
	defer rt.Close()
	handler := agentserver.NewDockerHandler(rt, "127.0.0.1", t.TempDir(), logger)

	res := handler.Create(context.Background(), &protocol.RequestMessage{
		UUID:             "uuid-escape-1",
		Action:           protocol.ActionCreate,
		ChallengeCode:    "escape",
		IsolationType:    "PER_USER",
		FlagType:         "STATIC",
		BuildContextPath: filepath.Join(t.TempDir(), "elsewhere"),
	})

	if res.Status != protocol.StatusFailed {
		t.Fatalf("허용 루트 밖 경로를 받아들임: %+v", res)
	}
}

// writeBusyboxBuildDir writes a Dockerfile + index.html into a "chal/" subfolder
// under root (mirroring the real challenge zip, which wraps its files) and
// returns root - the path the Provisioner would hand the Agent.
func writeBusyboxBuildDir(t *testing.T, root string) string {
	t.Helper()
	dockerfile := "FROM busybox\n" +
		"COPY index.html /www/index.html\n" +
		"EXPOSE 8080\n" +
		"CMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/www\"]\n"
	indexHTML := "<html><body>" + accessMarker + "</body></html>\n"

	dir := filepath.Join(root, "chal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	return root
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
