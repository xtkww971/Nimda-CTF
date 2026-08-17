package agentserver

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
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
	host string // the address participants connect to (e.g. "chal.nimda.kr")
}

func NewDockerHandler(rt *runtime.Docker, host string, log *slog.Logger) *DockerHandler {
	return &DockerHandler{rt: rt, host: host, log: log}
}

var _ Handler = (*DockerHandler)(nil)

func (h *DockerHandler) Create(ctx context.Context, req *protocol.CreateRequest, buildContext []byte) *protocol.CreateResult {
	code := sanitize(req.ChallengeCode)
	if code == "" {
		return failed(req.RequestID, "challengeCode가 비어있거나 허용되지 않는 문자만 있습니다")
	}
	image := "ctf-chal-" + code + ":latest"
	name := instanceName(code, req.IsolationType, req.UserID)

	h.log.Info("create 시작", "requestId", req.RequestID, "challengeCode", req.ChallengeCode,
		"isolation", req.IsolationType, "image", image, "name", name)

	if err := h.rt.BuildImage(ctx, buildContext, image); err != nil {
		return failed(req.RequestID, "이미지 빌드 실패: "+err.Error())
	}

	env := map[string]string{}
	if req.Flag != "" { // DYNAMIC: inject the per-user flag
		env["FLAG"] = req.Flag
	}

	// Labels let List report this instance later.
	labels := map[string]string{
		"nimda.ctf.code": req.ChallengeCode,
		"nimda.ctf.user": strconv.FormatInt(req.UserID, 10),
	}

	instanceID, port, err := h.rt.Run(ctx, image, name, env, labels)
	if err != nil {
		return failed(req.RequestID, "컨테이너 실행 실패: "+err.Error())
	}

	res := &protocol.CreateResult{
		RequestID:  req.RequestID,
		Status:     protocol.StatusReady,
		InstanceID: instanceID,
		Host:       h.host,
		Port:       port,
	}
	if req.IsolationType == "PER_USER" {
		res.ExpiresAt = time.Now().Add(perUserTTL).UTC().Format(time.RFC3339)
	}
	h.log.Info("create 완료", "requestId", req.RequestID, "instanceId", instanceID,
		"host", res.Host, "port", res.Port)
	return res
}

func (h *DockerHandler) Destroy(ctx context.Context, req *protocol.DestroyRequest) *protocol.DestroyResult {
	if err := h.rt.Remove(ctx, req.InstanceID); err != nil {
		return &protocol.DestroyResult{
			RequestID: req.RequestID,
			Status:    protocol.DestroyFailed,
			Message:   err.Error(),
		}
	}
	return &protocol.DestroyResult{RequestID: req.RequestID, Status: protocol.DestroyDone}
}

func (h *DockerHandler) Status(ctx context.Context, req *protocol.StatusRequest) *protocol.StatusResult {
	running, port, err := h.rt.Running(ctx, req.InstanceID)
	if err != nil {
		return &protocol.StatusResult{RequestID: req.RequestID, InstanceID: req.InstanceID,
			State: protocol.StateNotFound, Message: err.Error()}
	}
	if !running {
		return &protocol.StatusResult{RequestID: req.RequestID, InstanceID: req.InstanceID,
			State: protocol.StateNotFound}
	}
	return &protocol.StatusResult{
		RequestID:  req.RequestID,
		InstanceID: req.InstanceID,
		State:      protocol.StateRunning,
		Host:       h.host,
		Port:       port,
	}
}

func (h *DockerHandler) List(ctx context.Context, req *protocol.ListRequest) *protocol.ListResult {
	instances, err := h.rt.List(ctx)
	if err != nil {
		h.log.Warn("list 실패", "error", err)
		return &protocol.ListResult{RequestID: req.RequestID}
	}
	infos := make([]protocol.InstanceInfo, 0, len(instances))
	for _, in := range instances {
		infos = append(infos, protocol.InstanceInfo{
			InstanceID:    in.ID,
			ChallengeCode: in.ChallengeCode,
			UserID:        in.UserID,
			Host:          h.host,
			Port:          in.Port,
			State:         protocol.StateRunning,
		})
	}
	return &protocol.ListResult{RequestID: req.RequestID, Instances: infos}
}

func failed(requestID, msg string) *protocol.CreateResult {
	return &protocol.CreateResult{RequestID: requestID, Status: protocol.StatusFailed, Message: msg}
}

// 인스턴스 이름 설정 - PER_USER의 경우 인스턴스 이름 뒤 유저 ID를 붙임
func instanceName(code, isolation string, userID int64) string {
	if isolation == "PER_USER" {
		return fmt.Sprintf("ctf-%s-u%d", code, userID)
	}
	return "ctf-" + code
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
