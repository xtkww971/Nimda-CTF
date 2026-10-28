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

// pwn(TCP) 인스턴스에만 쓰는 설정. web(HTTP)은 쓰지 않는다.
type PwnOptions struct {
	Host    string // 참가자에게 보여줄 pwn 접속 주소 (CTF_PWN_PUBLIC_HOST)
	BindIP  string // 포트를 열 host IP (CTF_PWN_BIND_IP)
	Network string // 컨테이너를 붙일 네트워크 (CTF_PWN_NETWORK)
}

type DockerHandler struct {
	rt          *runtime.Docker
	log         *slog.Logger
	host        string     // Status가 보여주는 주소 (CTF_INSTANCE_HOST)
	instanceNet string     // web(HTTP) 인스턴스를 붙일 네트워크 (CTF_INSTANCE_NETWORK)
	pwn         PwnOptions // pwn(TCP) 전용 설정

	// buildRoot는 빌드 컨텍스트 경로가 반드시 그 아래여야 하는 허용 루트다. Provisioner가
	// 넘긴 경로를 그대로 믿으면 임의 호스트 경로(예: /)를 빌드 컨텍스트로 삼을 수 있으므로,
	// 공유 빌드 폴더 밖의 경로는 거부한다. 빈 값이면 검사를 건너뛴다(개발용).
	buildRoot string
}

func NewDockerHandler(rt *runtime.Docker, host, instanceNet, buildRoot string, pwn PwnOptions, log *slog.Logger) *DockerHandler {
	return &DockerHandler{rt: rt, host: host, instanceNet: instanceNet, buildRoot: buildRoot, pwn: pwn, log: log}
}

// 접속 방식에 따라 실행 옵션과 보여줄 host를 정한다.
// TCP(pwn)는 할당받은 포트를 지정 IP·ctf-pwn 네트워크로 열고 pwn 주소를 보여준다.
// HTTP(web)는 host 포트를 안 열고 ctf-instances에 붙는다. host는 Create가 컨테이너 이름으로
// 채우므로 여기선 빈 값을 돌려준다(ADR-0017: 프록시가 컨테이너 이름으로 붙는다).
func (h *DockerHandler) runParams(access protocol.AccessType, hostPort int) (runtime.RunSpec, string) {
	if access == protocol.AccessTCP {
		return runtime.RunSpec{BindIP: h.pwn.BindIP, HostPort: hostPort, Network: h.pwn.Network, Publish: true}, h.pwn.Host
	}
	return runtime.RunSpec{Network: h.instanceNet, Publish: false}, ""
}

// hostPort는 TCP일 때 dispatcher가 포트 풀에서 받아 넘긴 포트다. HTTP면 0이고 데몬이 고른다.
func (h *DockerHandler) Create(ctx context.Context, req *protocol.RequestMessage, hostPort int) *protocol.ResponseMessage {
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
		"nimda.ctf.code":  req.ChallengeCode,
		runtime.LabelUUID: req.UUID,
	}

	// PER_USER 만료 시각은 Run 전에 정한다. 응답에도 쓰고, reaper가 registry를 잃어도 회수할 수 있게
	// 컨테이너 라벨에도 박는다(ADR-0014의 orphan 회수).
	var expiresAt time.Time
	if req.IsolationType == "PER_USER" {
		expiresAt = time.Now().Add(perUserTTL).UTC()
		labels[runtime.LabelExpires] = expiresAt.Format(time.RFC3339)
	}

	spec, host := h.runParams(req.AccessType, hostPort)
	if req.AccessType != protocol.AccessTCP {
		// web은 짧은 alias로 붙는다(#39). 컨테이너 이름(ctf-{code}-{uuid})은 code가 길면
		// DNS 라벨 한계(63자)를 넘어 프록시가 못 붙는다. alias는 code 길이와 무관하게 짧다.
		alias := instanceAlias(string(req.IsolationType), code, req.UUID)
		if len(alias) > 63 {
			return failed(req.UUID, fmt.Sprintf("web 접속 이름(alias)이 63자를 넘습니다(%d자): %q", len(alias), alias))
		}
		spec.NetworkAlias = alias
		host = alias
	}
	instanceID, port, err := h.rt.Run(ctx, image, name, env, labels, spec)
	if err != nil {
		return failed(req.UUID, "컨테이너 실행 실패: "+err.Error())
	}

	res := &protocol.ResponseMessage{
		UUID:       req.UUID,
		Action:     protocol.ActionCreate,
		Status:     protocol.StatusReady,
		InstanceID: instanceID,
		Host:       host,
		Port:       port,
	}
	if !expiresAt.IsZero() {
		res.ExpiresAt = expiresAt.Format(time.RFC3339)
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
	running, port, _, err := h.rt.Running(ctx, req.InstanceID)
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
func (h *DockerHandler) Existing(ctx context.Context, uuid, instanceID string, access protocol.AccessType) *protocol.ResponseMessage {
	running, port, host, err := h.rt.Running(ctx, instanceID)
	if err != nil || !running {
		return nil
	}
	// 이미 떠 있는 컨테이너의 포트와 host를 그대로 쓴다. web은 네트워크 alias(#39),
	// pwn은 참가자에게 보여줄 pwn 주소다.
	if access == protocol.AccessTCP {
		host = h.pwn.Host
	}
	res := &protocol.ResponseMessage{
		UUID:       uuid,
		Action:     protocol.ActionCreate,
		Status:     protocol.StatusReady,
		InstanceID: instanceID,
		Host:       host,
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

// web(HTTP) 인스턴스가 ctf-instances에서 쓸 짧은 네트워크 alias(#39).
// PER_USER는 uuid로, SHARED는 code로 묶는다. 컨테이너 이름(ctf-{code}-{uuid})과 달리
// code 길이에 휘둘리지 않아 DNS 라벨 한계(63자)를 넘지 않는다.
func instanceAlias(isolation, code, uuid string) string {
	if isolation == "PER_USER" {
		return runtime.AliasPerUserPrefix + uuid
	}
	return runtime.AliasSharedPrefix + code
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
