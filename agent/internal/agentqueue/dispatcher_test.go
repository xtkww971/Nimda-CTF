package agentqueue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	protocol "nimda-ctf-protocol"
)

// 실제 도커 대신 "무엇을 시켰는지"만 기록한다.
type fakeInstances struct {
	createCalls  int
	destroyCalls int
	existing     *protocol.ResponseMessage // Existing이 돌려줄 값
	createResult *protocol.ResponseMessage
}

func (f *fakeInstances) Create(_ context.Context, req *protocol.RequestMessage) *protocol.ResponseMessage {
	f.createCalls++
	if f.createResult != nil {
		return f.createResult
	}
	return &protocol.ResponseMessage{
		UUID: req.UUID, Action: protocol.ActionCreate, Status: protocol.StatusReady,
		InstanceID: "new-container", Host: "127.0.0.1", Port: 32768,
	}
}

func (f *fakeInstances) Destroy(_ context.Context, uuid, instanceID string) *protocol.ResponseMessage {
	f.destroyCalls++
	if instanceID == "" {
		return &protocol.ResponseMessage{UUID: uuid, Action: protocol.ActionDestroy, Status: protocol.StatusNotFound}
	}
	return &protocol.ResponseMessage{UUID: uuid, Action: protocol.ActionDestroy, Status: protocol.StatusDestroyed}
}

func (f *fakeInstances) Existing(_ context.Context, _, _ string) *protocol.ResponseMessage {
	return f.existing
}

// 메모리 registry.
type fakeRegistry struct {
	saved   map[string]string
	readErr error
}

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{saved: map[string]string{}} }

func (r *fakeRegistry) Save(_ context.Context, uuid, instanceID string) error {
	r.saved[uuid] = instanceID
	return nil
}

func (r *fakeRegistry) InstanceID(_ context.Context, uuid string) (string, error) {
	if r.readErr != nil {
		return "", r.readErr
	}
	return r.saved[uuid], nil
}

func (r *fakeRegistry) Delete(_ context.Context, uuid string) error {
	delete(r.saved, uuid)
	return nil
}

func newDispatcher(inst *fakeInstances, reg *fakeRegistry) *Dispatcher {
	return NewDispatcher(inst, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// 빌드 컨텍스트로 쓸 폴더를 하나 만들어 경로를 돌려준다.
func buildDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ctf-unzip-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

func createReq(uuid, dir string) *protocol.RequestMessage {
	return &protocol.RequestMessage{
		UUID: uuid, Action: protocol.ActionCreate,
		ChallengeCode: "web-basic-01", IsolationType: "PER_USER", FlagType: "STATIC",
		BuildContextPath: dir,
	}
}

// 처음 오는 요청은 만들고 기록한다.
func TestCreateSavesInstanceID(t *testing.T) {
	inst, reg := &fakeInstances{}, newFakeRegistry()
	res, err := newDispatcher(inst, reg).Handle(context.Background(), createReq("uuid-1", buildDir(t)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inst.createCalls != 1 {
		t.Errorf("Create 호출 = %d, want 1", inst.createCalls)
	}
	if res.Status != protocol.StatusReady {
		t.Fatalf("status = %q, want READY", res.Status)
	}
	if reg.saved["uuid-1"] != "new-container" {
		t.Errorf("registry에 기록되지 않음: %+v", reg.saved)
	}
}

// 같은 uuid가 다시 배달되면 다시 만들지 않고 기존 것을 돌려준다.
//
// Redis Stream은 at-least-once다. 그대로 다시 만들면 runtime.Run이 같은 이름의 컨테이너를
// docker rm -f로 지우고 새로 띄우므로, 참가자가 풀고 있던 인스턴스가 초기화된다.
func TestCreateReusesRunningInstance(t *testing.T) {
	inst := &fakeInstances{existing: &protocol.ResponseMessage{
		UUID: "uuid-1", Action: protocol.ActionCreate, Status: protocol.StatusReady,
		InstanceID: "old-container", Host: "127.0.0.1", Port: 30000,
	}}
	reg := newFakeRegistry()
	reg.saved["uuid-1"] = "old-container"

	res, err := newDispatcher(inst, reg).Handle(context.Background(), createReq("uuid-1", buildDir(t)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if inst.createCalls != 0 {
		t.Errorf("이미 떠 있는데 다시 만들었습니다 (Create 호출 %d회)", inst.createCalls)
	}
	if res.InstanceID != "old-container" || res.Port != 30000 {
		t.Errorf("기존 접속 정보를 돌려주지 않음: %+v", res)
	}
}

// 기록은 있는데 컨테이너가 사라졌으면 다시 만든다.
func TestCreateRebuildsWhenInstanceGone(t *testing.T) {
	inst := &fakeInstances{existing: nil} // 살아있지 않다
	reg := newFakeRegistry()
	reg.saved["uuid-1"] = "dead-container"

	if _, err := newDispatcher(inst, reg).Handle(context.Background(), createReq("uuid-1", buildDir(t))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.createCalls != 1 {
		t.Errorf("컨테이너가 없는데 다시 만들지 않았습니다 (Create 호출 %d회)", inst.createCalls)
	}
	if reg.saved["uuid-1"] != "new-container" {
		t.Errorf("새 인스턴스로 기록이 갱신되지 않음: %+v", reg.saved)
	}
}

// registry를 못 읽으면 처리하지 않고 남긴다.
// 여기서 새로 만들면 같은 인스턴스가 둘이 될 수 있다.
func TestCreateFailsWhenRegistryUnreadable(t *testing.T) {
	inst := &fakeInstances{}
	reg := newFakeRegistry()
	reg.readErr = errors.New("redis down")

	_, err := newDispatcher(inst, reg).Handle(context.Background(), createReq("uuid-1", buildDir(t)))
	if err == nil {
		t.Fatal("기록을 못 읽었는데 처리했습니다")
	}
	if inst.createCalls != 0 {
		t.Errorf("기록을 못 읽었는데 만들었습니다 (Create 호출 %d회)", inst.createCalls)
	}
}

// 빌드가 끝나면 조율자가 풀어둔 폴더를 치운다. 안 치우면 ctf-build 볼륨이 계속 찬다.
func TestCreateCleansBuildDir(t *testing.T) {
	dir := buildDir(t)
	if _, err := newDispatcher(&fakeInstances{}, newFakeRegistry()).
		Handle(context.Background(), createReq("uuid-1", dir)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("빌드 폴더가 남아 있습니다: %s", dir)
	}
}

// 실패했어도 폴더는 치운다. 재시도는 조율자가 S3에서 다시 받아 다시 풀면서 시작한다.
func TestCreateCleansBuildDirOnFailure(t *testing.T) {
	dir := buildDir(t)
	inst := &fakeInstances{createResult: &protocol.ResponseMessage{
		UUID: "uuid-1", Action: protocol.ActionCreate,
		Status: protocol.StatusFailed, Message: "이미지 빌드 실패",
	}}

	res, err := newDispatcher(inst, newFakeRegistry()).Handle(context.Background(), createReq("uuid-1", dir))
	if err != nil {
		t.Fatalf("영구 실패는 에러가 아니라 응답이어야 함: %v", err)
	}
	if res.Status != protocol.StatusFailed {
		t.Errorf("status = %q, want FAILED", res.Status)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("실패했는데 빌드 폴더가 남아 있습니다: %s", dir)
	}
}

// DESTROY는 uuid만 들고 온다. 어느 컨테이너인지는 registry에서 찾고, 내린 뒤 기록도 지운다.
func TestDestroyRemovesRegistryEntry(t *testing.T) {
	inst, reg := &fakeInstances{}, newFakeRegistry()
	reg.saved["uuid-1"] = "some-container"

	res, err := newDispatcher(inst, reg).Handle(context.Background(),
		&protocol.RequestMessage{UUID: "uuid-1", Action: protocol.ActionDestroy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != protocol.StatusDestroyed {
		t.Fatalf("status = %q, want DESTROYED", res.Status)
	}
	if _, ok := reg.saved["uuid-1"]; ok {
		t.Errorf("내렸는데 기록이 남아 있습니다: %+v", reg.saved)
	}
}

// 기록이 없으면 NOT_FOUND. 이미 내렸거나 만든 적이 없는 경우다.
func TestDestroyWithoutRecord(t *testing.T) {
	res, err := newDispatcher(&fakeInstances{}, newFakeRegistry()).Handle(context.Background(),
		&protocol.RequestMessage{UUID: "uuid-none", Action: protocol.ActionDestroy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != protocol.StatusNotFound {
		t.Errorf("status = %q, want NOT_FOUND", res.Status)
	}
}
