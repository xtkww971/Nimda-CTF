package handler

import (
	"context"
	"log/slog"
	"os"

	protocol "nimda-ctf-protocol"

	"nimda-judge-provisioner/internal/agentclient"
	"nimda-judge-provisioner/internal/archive"
	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/queue"
)

// 이 핸들러가 S3에서 필요한 기능만 추린 인터페이스 (작게 잘라둬야 테스트에서 가짜로 갈아끼우기 쉽다).
type S3Downloader interface {
	GetFile(ctx context.Context, objectKey string) ([]byte, error)
}

// 작업 메시지를 받아 실제 처리로 보내는 핸들러.
//
// Router는 "무슨 작업인가"까지만 정하고, "그 작업을 누가 어떻게 하는가"는 여기서 갈린다.
// 분기를 한 군데로 모아둔 덕에 작업이 늘어나면 이 스위치와 task.go만 보면 된다.
//
// 인스턴스 생성은 이 타입이 직접 하고, 첨부파일 발급은 presign에게 넘긴다 — 둘이 쓰는
// 능력이 겹치지 않아서(도커·에이전트 vs S3 서명) 한 몸으로 둘 이유가 없다.
type TaskDispatcher struct {
	instance *Handler
	presign  *DownloadHandler
	log      *slog.Logger
}

func NewTaskDispatcher(instance *Handler, presign *DownloadHandler, log *slog.Logger) *TaskDispatcher {
	return &TaskDispatcher{instance: instance, presign: presign, log: log}
}

var _ TaskHandler = (*TaskDispatcher)(nil)

func (d *TaskDispatcher) Handle(ctx context.Context, msg TaskMessage) (*queue.Result, error) {
	switch m := msg.(type) {
	case *CreateInstanceMessage:
		return d.instance.Handle(ctx, m)

	case *GetPresignedURL:
		return d.presign.Handle(ctx, m)

	default:
		// TaskMessage를 구현한 타입이 늘었는데 여기 case를 안 붙인 경우다.
		// 컴파일러가 못 잡아주는 자리라, 조용히 넘기지 않고 실패로 알린다.
		d.log.ErrorContext(ctx, "처리할 수 없는 작업 종류",
			"uuid", msg.ID(), "task", msg.Kind())
		return &queue.Result{
			UUID:    msg.ID(),
			Status:  queue.StatusFailed,
			Message: "처리할 수 없는 작업 종류입니다: " + string(msg.Kind()),
		}, nil
	}
}

// 인스턴스가 필요한 문제를 에이전트에게 넘기는 핸들러.
type Handler struct {
	s3    S3Downloader
	agent agentclient.Dispatcher
	cfg   config.Config
	log   *slog.Logger
}

func NewHandler(s3 S3Downloader, agent agentclient.Dispatcher, cfg config.Config, log *slog.Logger) *Handler {
	return &Handler{s3: s3, agent: agent, cfg: cfg, log: log}
}

func (h *Handler) Handle(ctx context.Context, msg *CreateInstanceMessage) (*queue.Result, error) {
	// S3 키는 업로드 규칙 그대로 challenges/{code}/{code}.zip.
	objectKey := "challenges/" + msg.ChallengeCode + "/" + msg.ChallengeCode + ".zip"

	h.log.InfoContext(ctx, "create 처리 시작",
		"uuid", msg.UUID, "challengeCode", msg.ChallengeCode,
		"isolation", msg.IsolationType)

	// 1) 다운로드.
	zipData, err := h.s3.GetFile(ctx, objectKey)
	if err != nil {
		return h.failed(msg, "S3 다운로드 실패: "+err.Error()), nil
	}

	// 2) 공유 볼륨에 압축을 푼다. 에이전트가 이 경로에서 바로 빌드하므로 두 컨테이너가
	//    같은 경로로 이 폴더를 보고 있어야 한다(compose의 ctf-build 볼륨).
	dir, err := archive.UnzipBytesToDir(zipData, &h.cfg)
	if err != nil {
		return h.failed(msg, "압축 해제 실패: "+err.Error()), nil
	}

	// 3) 에이전트에게 넘긴다.
	err = h.agent.DispatchCreate(ctx, protocol.CreateRequest{
		UUID:             msg.UUID,
		ChallengeCode:    msg.ChallengeCode,
		IsolationType:    msg.IsolationType,
		FlagType:         msg.FlagType,
		BuildContextPath: dir,
		// Flag(DYNAMIC 주입값)은 플래그 설계 확정 후. 지금은 비움.
	})
	if err != nil {
		// 스트림에 넣지도 못했으면 에이전트는 이 요청을 모른다. 풀어둔 폴더를 쓸 사람이
		// 없으니 여기서 치우고, ACK하지 않도록 에러를 올려 재시도하게 둔다.
		os.RemoveAll(dir)
		h.log.WarnContext(ctx, "agent 전달 실패, 재시도 예정", "uuid", msg.UUID, "error", err)
		return nil, err
	}

	// todo 풀어둔 폴더는 누가 지우나. 소켓 시절엔 Create가 반환될 때 defer로 지웠지만
	//  이제 에이전트가 언제 빌드를 끝내는지 조율자가 모른다. 에이전트가 빌드 후 지우거나,
	//  결과를 받은 뒤 조율자가 지우거나 둘 중 하나로 정해야 한다.

	// 결과는 지금 없다. 에이전트가 결과 스트림에 넣으면 AgentResultConsumer가 발행한다.
	return nil, nil
}

func (h *Handler) failed(msg *CreateInstanceMessage, reason string) *queue.Result {
	h.log.Error("create 실패", "uuid", msg.UUID, "reason", reason)
	return &queue.Result{
		UUID:    msg.UUID,
		Status:  queue.StatusFailed,
		Message: reason,
	}
}

// 에이전트가 돌려준 결과를 웹 백엔드가 읽는 모양으로 옮긴다.
//
// 이제 둘 다 UUID로만 식별하므로 거의 그대로 흘려보내면 된다. 다른 건 에이전트가 말하는
// instanceId를 웹에 넘기지 않는다는 것뿐이다 — 웹은 host:port만 알면 된다.
type AgentResultMapper struct {
	log *slog.Logger
}

func NewAgentResultMapper(log *slog.Logger) *AgentResultMapper {
	return &AgentResultMapper{log: log}
}

var _ queue.AgentResultHandler = (*AgentResultMapper)(nil)

func (m *AgentResultMapper) Handle(ctx context.Context, res *protocol.CreateResult) (*queue.Result, error) {
	// todo 여기서 인스턴스 대장(어느 UUID가 어느 포트인지)을 갱신할 자리.
	//  ADR-0011의 서브도메인 프록시가 이 정보를 봐야 한다.
	return &queue.Result{
		UUID:      res.UUID,
		Status:    res.Status,
		Message:   res.Message,
		Host:      res.Host,
		Port:      res.Port,
		ExpiresAt: res.ExpiresAt,
	}, nil
}
