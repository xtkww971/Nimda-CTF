package agentserver

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"nimda-ctf-agent/internal/runtime"
	protocol "nimda-ctf-protocol"
)

// TODO TTL 설정 아직 안함
const perUserTTL = time.Hour

type DockerHandler struct {
	rt   *runtime.Docker
	log  *slog.Logger
	host string // 참가자가 접속하는 주소 (예: "chal.nimda.kr")

	// buildRoot는 빌드 컨텍스트 경로가 반드시 그 아래여야 하는 허용 루트다. Provisioner가
	// 넘긴 경로를 그대로 믿으면 임의 호스트 경로(예: /)를 빌드 컨텍스트로 삼을 수 있으므로,
	// 공유 빌드 폴더 밖의 경로는 거부한다. 빈 값이면 검사를 건너뛴다(개발용).
	buildRoot string
}

func NewDockerHandler(rt *runtime.Docker, host, buildRoot string, log *slog.Logger) *DockerHandler {
	return &DockerHandler{rt: rt, host: host, buildRoot: buildRoot, log: log}
}

func (h *DockerHandler) Create(ctx context.Context, req *protocol.RequestMessage) *protocol.ResponseMessage {
	code := sanitize(req.ChallengeCode)
	if code == "" {
		return failed(req.UUID, "challengeCode가 비어있거나 허용되지 않는 문자만 있습니다")
	}
	if err := h.checkBuildPath(req.BuildContextPath); err != nil {
		return failed(req.UUID, err.Error())
	}
	image := "ctf-chal-" + code + ":latest"
	name, err := instanceName(code, string(req.IsolationType), req.UUID)
	if err != nil {
		return failed(req.UUID, err.Error())
	}

	h.log.Info("create 시작", "uuid", req.UUID, "challengeCode", req.ChallengeCode,
		"isolation", req.IsolationType, "image", image, "name", name, "buildPath", req.BuildContextPath)

	if err := h.rt.BuildImageFromDir(ctx, req.BuildContextPath, image); err != nil {
		return failed(req.UUID, "이미지 빌드 실패: "+err.Error())
	}

	env := map[string]string{}
	if req.Flag != "" { // DYNAMIC이면 유저별 플래그를 컨테이너에 넣어준다
		env["FLAG"] = req.Flag
	}

	// 나중에 List가 이 인스턴스를 알아보게 라벨을 붙인다.
	// 사용자 ID 대신 UUID를 남긴다 — 에이전트는 요청한 사람이 누구인지 모르고, 알 필요도 없다.
	labels := map[string]string{
		"nimda.ctf.code": req.ChallengeCode,
		"nimda.ctf.uuid": req.UUID,
	}

	// TODO(이슈 B 2단계): TCP면 할당 포트·바인딩 IP·ctf-pwn 네트워크로 채운다. 지금은 web과 동일.
	instanceID, port, err := h.rt.Run(ctx, image, name, env, labels, runtime.RunSpec{})
	if err != nil {
		return failed(req.UUID, "컨테이너 실행 실패: "+err.Error())
	}

	res := &protocol.ResponseMessage{
		UUID:       req.UUID,
		Action:     protocol.ActionCreate,
		Status:     protocol.StatusReady,
		InstanceID: instanceID,
		Host:       h.host,
		Port:       port,
	}
	if req.IsolationType == "PER_USER" {
		res.ExpiresAt = time.Now().Add(perUserTTL).UTC().Format(time.RFC3339)
	}
	h.log.Info("create 완료", "uuid", req.UUID, "instanceId", instanceID,
		"host", res.Host, "port", res.Port)
	return res
}

// 인스턴스를 내린다. 어느 컨테이너인지는 부르는 쪽이 registry에서 찾아 넘긴다.
func (h *DockerHandler) Destroy(ctx context.Context, uuid, instanceID string) *protocol.ResponseMessage {
	if instanceID == "" {
		// 기록이 없다. 이미 내렸거나 애초에 만든 적이 없다 — 둘 다 "지금은 없다"로 같다.
		return &protocol.ResponseMessage{
			UUID:   uuid,
			Action: protocol.ActionDestroy,
			Status: protocol.StatusNotFound,
		}
	}
	if err := h.rt.Remove(ctx, instanceID); err != nil {
		return &protocol.ResponseMessage{
			UUID:    uuid,
			Action:  protocol.ActionDestroy,
			Status:  protocol.StatusFailed,
			Message: err.Error(),
		}
	}
	return &protocol.ResponseMessage{UUID: uuid, Action: protocol.ActionDestroy, Status: protocol.StatusDestroyed}
}

func (h *DockerHandler) Status(ctx context.Context, req *protocol.StatusRequest) *protocol.StatusResult {
	running, port, err := h.rt.Running(ctx, req.InstanceID)
	if err != nil {
		return &protocol.StatusResult{UUID: req.UUID, InstanceID: req.InstanceID,
			State: protocol.StateNotFound, Message: err.Error()}
	}
	if !running {
		return &protocol.StatusResult{UUID: req.UUID, InstanceID: req.InstanceID,
			State: protocol.StateNotFound}
	}
	return &protocol.StatusResult{
		UUID:       req.UUID,
		InstanceID: req.InstanceID,
		State:      protocol.StateRunning,
		Host:       h.host,
		Port:       port,
	}
}

// 빌드 경로가 비었거나 허용된 루트를 벗어나면 거부한다. 그래야 provisioner가 뚫려도
// 임의 호스트 경로(예: "/")를 빌드 컨텍스트로 못 삼는다.
func (h *DockerHandler) checkBuildPath(path string) error {
	if path == "" {
		return fmt.Errorf("buildContextPath가 비어있습니다")
	}
	if h.buildRoot == "" {
		return nil // 개발용: 루트 미설정이면 검사 생략
	}
	clean := filepath.Clean(path)
	root := filepath.Clean(h.buildRoot)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return fmt.Errorf("빌드 경로가 허용된 루트(%s) 밖입니다: %s", h.buildRoot, path)
	}
	return nil
}

func failed(uuid, msg string) *protocol.ResponseMessage {
	return &protocol.ResponseMessage{
		UUID:    uuid,
		Action:  protocol.ActionCreate,
		Status:  protocol.StatusFailed,
		Message: msg,
	}
}

// 이미 떠 있는 인스턴스를 그대로 돌려준다. 재배달이나 중복 요청일 때 쓴다.
func (h *DockerHandler) Existing(ctx context.Context, uuid, instanceID string) *protocol.ResponseMessage {
	running, port, err := h.rt.Running(ctx, instanceID)
	if err != nil || !running {
		return nil
	}
	res := &protocol.ResponseMessage{
		UUID:       uuid,
		Action:     protocol.ActionCreate,
		Status:     protocol.StatusReady,
		InstanceID: instanceID,
		Host:       h.host,
		Port:       port,
	}
	h.log.Info("이미 떠 있는 인스턴스를 재사용", "uuid", uuid, "instanceId", instanceID, "port", port)
	return res
}

// 컨테이너 이름에 쓸 수 있는 글자. 도커 이름 규칙이기도 하고, UUID를 그대로 이어붙이므로
// 이상한 문자가 명령줄로 새어 들어가지 않게 막는 자리이기도 하다.
var uuidPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// 인스턴스 이름 설정 - PER_USER는 뒤에 UUID를 붙여 사용자마다 다른 컨테이너가 되게 한다.
//
// 예전에는 userId를 붙였다. 웹이 UUID ↔ 사용자 매핑을 자기 쪽에서 관리하기로 하면서
// 에이전트는 사용자가 누구인지 모르게 됐고, 이름의 구분자도 UUID가 됐다.
//
// 주의: 이 이름이 같으면 같은 컨테이너로 본다(runtime.Run이 같은 이름을 먼저 지우고 띄운다).
// 즉 "한 사용자가 한 문제에 인스턴스 하나"는 웹이 같은 사용자·같은 문제에 같은 UUID를
// 주는 동안에만 성립한다. 요청마다 새 UUID를 발급하면 요청마다 새 컨테이너가 뜬다.
func instanceName(code, isolation, uuid string) (string, error) {
	if isolation != "PER_USER" {
		return "ctf-" + code, nil
	}
	if !uuidPattern.MatchString(uuid) {
		return "", fmt.Errorf("컨테이너 이름에 쓸 수 없는 uuid입니다: %q", uuid)
	}
	return fmt.Sprintf("ctf-%s-%s", code, uuid), nil
}

// ChallengeCode 공격 필터링 - 문자만 들어갈 수 있게 허용
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}
