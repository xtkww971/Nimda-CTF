package test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nimda-ctf-agent/internal/agentserver"
	"nimda-ctf-agent/internal/runtime"
	protocol "nimda-ctf-protocol"
)

const accessMarker = "CTF_AGENT_ACCESS_OK"

// TestAgentBuildsRunsAndIsReachable drives the whole Agent Create path against
// real Docker: given a build context, the Agent builds an image, runs a
// container, and returns host:port. The test then proves the instance is
// reachable and finishes with Status (RUNNING) and Destroy.
//
// web(HTTP) 인스턴스는 더 이상 host 포트에 publish되지 않는다(#35, ADR-0017). ctf-instances
// 네트워크에 붙고, 프록시가 같은 네트워크에서 컨테이너 이름으로 붙는다. 그래서 이 테스트도
// 127.0.0.1:host포트가 아니라, 같은 네트워크에 임시 클라이언트 컨테이너를 띄워 컨테이너
// 이름으로 접속되는지를 확인한다.
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

	// web 인스턴스가 붙을 네트워크. 운영의 ctf-instances와 같은 internal 네트워크를
	// 테스트용 이름으로 따로 만든다. 프록시 역할의 클라이언트 컨테이너도 여기에 붙인다.
	netName := fmt.Sprintf("ctf-instances-test%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "network", "create", "--internal", netName).CombinedOutput(); err != nil {
		t.Skipf("테스트 네트워크 생성 실패: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", netName).Run() })

	// 조율자가 압축을 푸는 공유 폴더 역할. Agent는 이 루트 아래 경로만 빌드한다.
	buildRoot := t.TempDir()
	handler := agentserver.NewDockerHandler(rt, "127.0.0.1", netName, buildRoot, agentserver.PwnOptions{}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A unique code -> unique image/container name, so runs don't collide.
	code := fmt.Sprintf("agenttest%d", time.Now().Unix())
	image := "ctf-chal-" + code + ":latest"
	// 웹이 사용자·문제마다 발급하는 값 자리. PER_USER 컨테이너 이름의 구분자가 된다.
	uuid := fmt.Sprintf("uuid-%d", time.Now().UnixNano())
	name := "ctf-" + code + "-" + uuid // PER_USER 컨테이너 이름 = web 접속 host
	t.Cleanup(func() {
		bg := context.Background()
		_ = rt.Remove(bg, name)
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
	}, 0)

	if res == nil {
		t.Fatal("create가 결과를 반환하지 않음")
	}
	if res.Status != protocol.StatusReady {
		t.Fatalf("status = %q, want READY (message=%q)", res.Status, res.Message)
	}
	// web은 컨테이너 이름을 host로, 컨테이너가 EXPOSE한 포트(8080)를 포트로 돌려준다.
	if res.Host != name {
		t.Fatalf("web host가 컨테이너 이름이 아님: got %q want %q", res.Host, name)
	}
	if res.Port != 8080 {
		t.Fatalf("web 포트가 EXPOSE 포트(8080)가 아님: %d", res.Port)
	}
	if res.InstanceID == "" {
		t.Fatal("instanceID가 비어있음")
	}
	if res.ExpiresAt == "" {
		t.Errorf("PER_USER 인스턴스는 만료 시각이 있어야 함")
	}
	// host 포트를 열지 않아야 한다(ADR-0017). inspect로 PortBindings가 비었는지 본다.
	assertNoPublishedPort(t, res.InstanceID)
	t.Logf("인스턴스 생성됨: %s:%d (id=%s)", res.Host, res.Port, res.InstanceID)

	// ── 접근 확인 (핵심) ────────────────────────────────────────────────
	// 같은 네트워크에 임시 클라이언트 컨테이너를 띄워, 컨테이너 이름으로 붙는다.
	url := fmt.Sprintf("http://%s:%d/", res.Host, res.Port)
	body := wgetFromNetwork(t, netName, url, 20*time.Second)
	if !bytes.Contains(body, []byte(accessMarker)) {
		t.Fatalf("접근은 됐지만 응답에 마커가 없음: %q", string(body))
	}
	t.Logf("접근 확인 완료: %s(네트워크 %s) -> 마커 응답", url, netName)

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

	// 삭제 후에는 이름이 안 풀려 접근이 안 돼야 한다.
	if _, err := tryWgetOnce(netName, url); err == nil {
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
	handler := agentserver.NewDockerHandler(rt, "127.0.0.1", "", t.TempDir(), agentserver.PwnOptions{}, logger)

	res := handler.Create(context.Background(), &protocol.RequestMessage{
		UUID:             "uuid-escape-1",
		Action:           protocol.ActionCreate,
		ChallengeCode:    "escape",
		IsolationType:    "PER_USER",
		FlagType:         "STATIC",
		BuildContextPath: filepath.Join(t.TempDir(), "elsewhere"),
	}, 0)

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

// 같은 네트워크에 임시 클라이언트 컨테이너를 띄워 url에 붙어 본다. 프록시가 할 일을 흉내낸다.
// 컨테이너가 뜨고 httpd가 응답할 때까지 timeout 안에서 다시 시도한다.
func wgetFromNetwork(t *testing.T, netName, url string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := tryWgetOnce(netName, url)
		if err == nil {
			return out
		}
		last = err.Error() + ": " + string(out)
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s 접근 실패 (%s 내): %s", url, timeout, last)
	return nil
}

func tryWgetOnce(netName, url string) ([]byte, error) {
	// busybox wget: -q 조용히, -O- 본문을 stdout으로, -T 3 타임아웃.
	// internal 네트워크라도 같은 네트워크의 컨테이너끼리는 이름으로 닿는다.
	cmd := exec.Command("docker", "run", "--rm", "--network", netName,
		"busybox", "wget", "-q", "-O-", "-T", "3", url)
	return cmd.CombinedOutput()
}

// web 인스턴스가 host 포트를 열지 않았는지 확인한다. 열려 있으면 PortBindings에 매핑이 있다.
func assertNoPublishedPort(t *testing.T, id string) {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{json .HostConfig.PortBindings}}", id).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect 실패: %v (%s)", err, out)
	}
	// PortBindings가 없으면 "null" 또는 "{}"다.
	s := strings.TrimSpace(string(out))
	if s != "null" && s != "{}" {
		t.Fatalf("web 인스턴스가 host 포트를 열었다: PortBindings=%s", s)
	}
}
