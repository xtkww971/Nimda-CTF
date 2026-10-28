package agentqueue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nimda-ctf-agent/internal/registry"
	protocol "nimda-ctf-protocol"
)

// 실제 도커 대신 "무엇을 시켰는지"만 기록한다.
type fakeInstances struct {
	createCalls  int
	destroyCalls int
	lastPort     int // Create에 넘어온 hostPort (TCP 분기 검증용)
	existing     *protocol.ResponseMessage // Existing이 돌려줄 값
	createResult *protocol.ResponseMessage
}

func (f *fakeInstances) Create(_ context.Context, req *protocol.RequestMessage, hostPort int) *protocol.ResponseMessage {
	f.createCalls++
	f.lastPort = hostPort
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

func (f *fakeInstances) Existing(_ context.Context, _, _ string, _ protocol.AccessType) *protocol.ResponseMessage {
	return f.existing
}

// 메모리 registry.
type fakeRegistry struct {
	saved   map[string]string // PER_USER: uuid → instanceID
	readErr error

	joined  map[string]string               // SHARED: uuid → challengeCode
	leases  map[string]map[string]time.Time // code → uuid → 만료 시각
	shared  map[string]string               // code → instanceID
	locked  map[string]bool                 // code → lock 잡힘
	unlocks int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		saved:  map[string]string{},
		joined: map[string]string{},
		leases: map[string]map[string]time.Time{},
		shared: map[string]string{},
		locked: map[string]bool{},
	}
}

func (r *fakeRegistry) Save(_ context.Context, uuid, instanceID string, port int) error {
	r.saved[uuid] = instanceID
	_ = port // 포트를 받아 넘기는 분기는 다음 이슈. 지금은 0만 온다.
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

func (r *fakeRegistry) Entry(_ context.Context, uuid string) (*registry.Entry, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	if id, ok := r.saved[uuid]; ok {
		return &registry.Entry{Isolation: protocol.IsolationPerUser, InstanceID: id}, nil
	}
	if code, ok := r.joined[uuid]; ok {
		return &registry.Entry{Isolation: protocol.IsolationShared, ChallengeCode: code,
			ExpiresAt: r.leases[code][uuid]}, nil
	}
	return nil, nil
}

func (r *fakeRegistry) Shared(_ context.Context, code string) (*registry.SharedInstance, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	id, ok := r.shared[code]
	if !ok {
		return nil, nil
	}
	return &registry.SharedInstance{InstanceID: id}, nil
}

func (r *fakeRegistry) SaveShared(_ context.Context, code, instanceID string) error {
	r.shared[code] = instanceID
	return nil
}

// 실제 registry처럼 이미 lease가 있으면 만료 시각을 바꾸지 않는다(ZADD NX).
func (r *fakeRegistry) Join(_ context.Context, code, uuid string, expiresAt time.Time) (time.Time, error) {
	if r.leases[code] == nil {
		r.leases[code] = map[string]time.Time{}
	}
	if existing, ok := r.leases[code][uuid]; ok {
		return existing, nil
	}
	r.leases[code][uuid] = expiresAt
	r.joined[uuid] = code
	return expiresAt, nil
}

func (r *fakeRegistry) Leave(_ context.Context, code, uuid string) error {
	delete(r.leases[code], uuid)
	delete(r.joined, uuid)
	return nil
}

func (r *fakeRegistry) Lock(_ context.Context, code string, _ time.Duration) (string, bool, error) {
	if r.locked[code] {
		return "", false, nil
	}
	r.locked[code] = true
	return "token-" + code, true, nil
}

func (r *fakeRegistry) Unlock(_ context.Context, code, _ string) error {
	r.locked[code] = false
	r.unlocks++
	return nil
}

func newDispatcher(inst *fakeInstances, reg *fakeRegistry) *Dispatcher {
	d := NewDispatcher(inst, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// lock 대기 테스트가 오래 걸리지 않게 줄인다.
	d.lockRetryInterval = 5 * time.Millisecond
	d.lockWaitTimeout = 20 * time.Millisecond
	return d
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

// ── SHARED (ADR-0019) ──────────────────────────────────────────────────────

func sharedReq(uuid, dir string) *protocol.RequestMessage {
	r := createReq(uuid, dir)
	r.IsolationType = protocol.IsolationShared
	return r
}

// 첫 참가자는 빌드하고, shared:{code}와 lease를 저장한다.
func TestSharedFirstJoinBuildsAndSaves(t *testing.T) {
	inst, reg := &fakeInstances{}, newFakeRegistry()
	res, err := newDispatcher(inst, reg).Handle(context.Background(), sharedReq("uuid-a", buildDir(t)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != protocol.StatusReady {
		t.Fatalf("status = %q (%s), want READY", res.Status, res.Message)
	}
	if inst.createCalls != 1 {
		t.Errorf("Create 호출 = %d, want 1", inst.createCalls)
	}
	if reg.shared["web-basic-01"] != "new-container" {
		t.Errorf("shared:{code}가 저장되지 않음: %+v", reg.shared)
	}
	if _, ok := reg.leases["web-basic-01"]["uuid-a"]; !ok {
		t.Errorf("lease가 저장되지 않음: %+v", reg.leases)
	}
	if res.ExpiresAt == "" {
		t.Error("SHARED 응답에 expiresAt(lease 만료 시각)이 없음")
	}
	if reg.locked["web-basic-01"] {
		t.Error("lock이 풀리지 않음")
	}
}

// 두 번째 참가자는 빌드하지 않고 같은 컨테이너에 붙는다. PER_USER처럼 지우고 다시 만들면 안 된다.
func TestSharedSecondJoinReusesContainer(t *testing.T) {
	inst := &fakeInstances{existing: &protocol.ResponseMessage{
		Action: protocol.ActionCreate, Status: protocol.StatusReady,
		InstanceID: "shared-container", Host: "127.0.0.1", Port: 30000,
	}}
	reg := newFakeRegistry()
	reg.shared["web-basic-01"] = "shared-container"

	res, err := newDispatcher(inst, reg).Handle(context.Background(), sharedReq("uuid-b", buildDir(t)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.createCalls != 0 {
		t.Errorf("떠 있는데 다시 빌드함 (Create 호출 %d회)", inst.createCalls)
	}
	if res.UUID != "uuid-b" || res.InstanceID != "shared-container" || res.Port != 30000 {
		t.Errorf("기존 공유 컨테이너 정보가 아님: %+v", res)
	}
	if _, ok := reg.leases["web-basic-01"]["uuid-b"]; !ok {
		t.Error("두 번째 참가자의 lease가 없음")
	}
}

// 기록은 있는데 공유 컨테이너가 죽었으면 다시 만들고 instanceId를 교체한다.
func TestSharedRebuildsWhenContainerGone(t *testing.T) {
	inst := &fakeInstances{existing: nil}
	reg := newFakeRegistry()
	reg.shared["web-basic-01"] = "dead-container"

	if _, err := newDispatcher(inst, reg).Handle(context.Background(), sharedReq("uuid-a", buildDir(t))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.createCalls != 1 {
		t.Errorf("Create 호출 = %d, want 1", inst.createCalls)
	}
	if reg.shared["web-basic-01"] != "new-container" {
		t.Errorf("instanceId가 교체되지 않음: %+v", reg.shared)
	}
}

// 같은 요청이 redelivery되면 lease가 연장되면 안 된다. 응답의 expiresAt도 처음 값이어야 한다.
func TestSharedRedeliveryKeepsLease(t *testing.T) {
	inst, reg := &fakeInstances{}, newFakeRegistry()
	d := newDispatcher(inst, reg)
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return base }

	first, _ := d.Handle(context.Background(), sharedReq("uuid-a", buildDir(t)))

	inst.existing = &protocol.ResponseMessage{Status: protocol.StatusReady, InstanceID: "new-container"}
	d.now = func() time.Time { return base.Add(30 * time.Minute) }
	second, _ := d.Handle(context.Background(), sharedReq("uuid-a", buildDir(t)))

	if first.ExpiresAt != second.ExpiresAt {
		t.Errorf("redelivery로 lease가 바뀜: %s → %s", first.ExpiresAt, second.ExpiresAt)
	}
}

// lock을 못 잡으면 잠깐 기다리다가 FAILED. 웹이 PENDING에 갇히지 않게 에러가 아니라 응답으로 준다.
func TestSharedLockTimeoutReturnsFailed(t *testing.T) {
	inst, reg := &fakeInstances{}, newFakeRegistry()
	reg.locked["web-basic-01"] = true // 다른 쪽이 잡고 있다
	dir := buildDir(t)

	res, err := newDispatcher(inst, reg).Handle(context.Background(), sharedReq("uuid-a", dir))
	if err != nil {
		t.Fatalf("lock 실패는 에러가 아니라 FAILED 응답이어야 함: %v", err)
	}
	if res.Status != protocol.StatusFailed {
		t.Errorf("status = %q, want FAILED", res.Status)
	}
	if inst.createCalls != 0 {
		t.Errorf("lock 없이 빌드함 (Create 호출 %d회)", inst.createCalls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("빌드 폴더가 남아 있습니다: %s", dir)
	}
}

// SHARED DESTROY는 자기 lease만 반납한다. 컨테이너와 다른 참가자는 그대로다.
func TestSharedDestroyOnlyLeaves(t *testing.T) {
	inst, reg := &fakeInstances{}, newFakeRegistry()
	reg.shared["web-basic-01"] = "shared-container"
	exp := time.Now().Add(time.Hour)
	reg.Join(context.Background(), "web-basic-01", "uuid-a", exp)
	reg.Join(context.Background(), "web-basic-01", "uuid-b", exp)

	res, err := newDispatcher(inst, reg).Handle(context.Background(),
		&protocol.RequestMessage{UUID: "uuid-a", Action: protocol.ActionDestroy})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != protocol.StatusDestroyed {
		t.Errorf("status = %q, want DESTROYED", res.Status)
	}
	if inst.destroyCalls != 0 {
		t.Errorf("공유 컨테이너를 지움 (Destroy 호출 %d회)", inst.destroyCalls)
	}
	if _, ok := reg.leases["web-basic-01"]["uuid-a"]; ok {
		t.Error("a의 lease가 남아 있음")
	}
	if _, ok := reg.leases["web-basic-01"]["uuid-b"]; !ok {
		t.Error("b의 lease가 사라짐")
	}
	if reg.shared["web-basic-01"] != "shared-container" {
		t.Error("공유 컨테이너 기록이 사라짐")
	}
}

// 모르는 isolationType은 만들지 않는다. 덜 격리된 쪽으로 흘려보내면 안 된다.
func TestCreateRejectsUnknownIsolation(t *testing.T) {
	inst := &fakeInstances{}
	req := createReq("uuid-x", buildDir(t))
	req.IsolationType = "PER-USER"

	res, err := newDispatcher(inst, newFakeRegistry()).Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != protocol.StatusFailed {
		t.Errorf("status = %q, want FAILED", res.Status)
	}
	if inst.createCalls != 0 {
		t.Errorf("모르는 값인데 만들었음 (Create 호출 %d회)", inst.createCalls)
	}
}
