package protocol

import "testing"

// Redis Stream은 평평한 문자열 맵만 싣는다. 여기서 확인하려는 건 "한 바퀴 돌려도 값이
// 그대로냐" 하나다 — 조율자가 실어 보낸 걸 에이전트가 같은 값으로 읽어야 한다.

func createRequest() RequestMessage {
	return RequestMessage{
		UUID:             "9f1c-req-1",
		Action:           ActionCreate,
		ChallengeCode:    "web-basic-01",
		IsolationType:    "PER_USER",
		AccessType:       AccessHTTP,
		FlagType:         "DYNAMIC",
		Flag:             "FLAG{abc}",
		BuildContextPath: "/var/lib/ctf/build/9f1c-req-1",
	}
}

func TestCreateRequestRoundTrip(t *testing.T) {
	in := createRequest()

	out, err := ParseRequestMessage(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the request:\n got %+v\nwant %+v", *out, in)
	}
}

// STATIC 문제는 flag를 안 싣는다. 빈 값이 "FLAG=" 로 주입되면 안 되므로 빠진 채로 와야 한다.
func TestCreateRequestOmitsEmptyFlag(t *testing.T) {
	in := createRequest()
	in.Flag = ""
	in.FlagType = "STATIC"

	values := in.ToStreamValues()
	if _, ok := values["flag"]; ok {
		t.Error("flag가 비었는데도 스트림에 실렸습니다")
	}
	out, err := ParseRequestMessage(values)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Flag != "" {
		t.Errorf("flag = %q, want empty", out.Flag)
	}
}

// CREATE는 빌드에 필요한 것이 다 있어야 한다. 하나라도 없으면 에이전트가 할 수 있는 일이
// 없는데, 조용히 넘어가면 이름이 빈 이미지를 빌드하려 든다.
func TestCreateRequestRejectsMissingFields(t *testing.T) {
	base := createRequest().ToStreamValues()

	for _, key := range []string{"uuid", "action", "challengeCode", "isolationType", "accessType", "flagType", "buildContextPath"} {
		values := map[string]interface{}{}
		for k, v := range base {
			values[k] = v
		}
		delete(values, key)

		if _, err := ParseRequestMessage(values); err == nil {
			t.Errorf("%q가 빠졌는데 파싱이 성공했습니다", key)
		}
	}
}

// DESTROY는 uuid만 있으면 된다. 어느 컨테이너인지는 에이전트가 registry에서 찾는다.
// CREATE와 같은 필드를 요구하면 멀쩡한 삭제 요청이 독성 메시지가 된다.
func TestDestroyRequestNeedsOnlyUUID(t *testing.T) {
	in := RequestMessage{UUID: "9f1c-req-7", Action: ActionDestroy}

	out, err := ParseRequestMessage(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the request:\n got %+v\nwant %+v", *out, in)
	}
}

func TestRequestRejectsUnknownAction(t *testing.T) {
	values := RequestMessage{UUID: "u-1", Action: ActionDestroy}.ToStreamValues()
	values["action"] = "REBOOT"

	if _, err := ParseRequestMessage(values); err == nil {
		t.Error("모르는 action인데 파싱이 성공했습니다")
	}
}

// SHARED도 PER_USER와 같은 메시지 모양이다. 값만 달라지고 한 바퀴 돌려도 그대로여야 한다.
func TestSharedRequestRoundTrip(t *testing.T) {
	in := createRequest()
	in.IsolationType = IsolationShared
	in.FlagType = FlagStatic
	in.Flag = ""

	out, err := ParseRequestMessage(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the request:\n got %+v\nwant %+v", *out, in)
	}
}

// 모르는 격리 방식은 어느 쪽으로도 흘려보내지 않는다. 오타가 공용 컨테이너로 이어지면
// pwn 문제가 모두에게 열린다.
func TestCreateRequestRejectsUnknownIsolation(t *testing.T) {
	for _, v := range []string{"PER-USER", "shared", "NONE"} {
		values := createRequest().ToStreamValues()
		values["isolationType"] = v

		if _, err := ParseRequestMessage(values); err == nil {
			t.Errorf("isolationType=%q인데 파싱이 성공했습니다", v)
		}
	}
}

// 공유 컨테이너는 하나라 플래그도 하나다. DYNAMIC을 받으면 참가자 플래그가 서로 덮어쓴다.
func TestCreateRequestRejectsSharedDynamic(t *testing.T) {
	in := createRequest()
	in.IsolationType = IsolationShared
	in.FlagType = FlagDynamic

	if _, err := ParseRequestMessage(in.ToStreamValues()); err == nil {
		t.Error("SHARED + DYNAMIC인데 파싱이 성공했습니다")
	}
}

// pwn은 TCP + PER_USER로 온다. 값만 달라지고 한 바퀴 돌려도 그대로여야 한다.
func TestCreateRequestTCPRoundTrip(t *testing.T) {
	in := createRequest()
	in.AccessType = AccessTCP
	in.FlagType = FlagStatic
	in.Flag = ""

	out, err := ParseRequestMessage(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the request:\n got %+v\nwant %+v", *out, in)
	}
}

// 모르는 접속 방식은 흘려보내지 않는다. 오타가 TCP로 처리되면 web 문제 포트가 밖에 열린다.
func TestCreateRequestRejectsUnknownAccess(t *testing.T) {
	for _, v := range []string{"http", "tcp", "RAW", "WS"} {
		values := createRequest().ToStreamValues()
		values["accessType"] = v

		if _, err := ParseRequestMessage(values); err == nil {
			t.Errorf("accessType=%q인데 파싱이 성공했습니다", v)
		}
	}
}

// pwn은 셸을 따는 게 정답이라 포트를 밖에 연다. SHARED로 뜨면 한 명이 모두의 문제를 망친다.
func TestCreateRequestRejectsTCPShared(t *testing.T) {
	in := createRequest()
	in.AccessType = AccessTCP
	in.IsolationType = IsolationShared
	in.FlagType = FlagStatic
	in.Flag = ""

	if _, err := ParseRequestMessage(in.ToStreamValues()); err == nil {
		t.Error("TCP + SHARED인데 파싱이 성공했습니다")
	}
}

func TestResponseRoundTrip(t *testing.T) {
	in := ResponseMessage{
		UUID:       "9f1c-req-1",
		Action:     ActionCreate,
		Status:     StatusReady,
		InstanceID: "c0ffee",
		Host:       "chal.nimda.kr",
		Port:       32768,
		ExpiresAt:  "2026-09-09T12:00:00Z",
	}

	out, err := ParseResponseMessage(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the response:\n got %+v\nwant %+v", *out, in)
	}
}

// 실패 응답은 host/port/expiresAt가 전부 빈 채로 온다. 0번 포트로 읽히면 안 된다.
func TestResponseFailedHasNoHostPort(t *testing.T) {
	in := ResponseMessage{
		UUID:    "9f1c-req-4",
		Action:  ActionCreate,
		Status:  StatusFailed,
		Message: "이미지 빌드 실패",
	}

	values := in.ToStreamValues()
	for _, key := range []string{"host", "port", "expiresAt", "instanceId"} {
		if _, ok := values[key]; ok {
			t.Errorf("실패 응답에 %q가 실렸습니다", key)
		}
	}

	out, err := ParseResponseMessage(values)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Port != 0 || out.Host != "" {
		t.Errorf("실패 응답에 host/port가 채워졌습니다: %+v", *out)
	}
	if out.Message != in.Message {
		t.Errorf("message = %q, want %q", out.Message, in.Message)
	}
}

// 포트가 숫자가 아니면 접속 못 하는 인스턴스를 READY로 넘기게 된다. 조용히 0으로 두지 않는다.
func TestResponseRejectsBadPort(t *testing.T) {
	values := ResponseMessage{UUID: "u-5", Action: ActionCreate, Status: StatusReady, Host: "h", Port: 1}.ToStreamValues()
	values["port"] = "not-a-number"

	if _, err := ParseResponseMessage(values); err == nil {
		t.Error("포트가 숫자가 아닌데 파싱이 성공했습니다")
	}
}

func TestResponseRejectsUnknownStatus(t *testing.T) {
	values := ResponseMessage{UUID: "u-6", Action: ActionCreate, Status: StatusReady}.ToStreamValues()
	values["status"] = "MAYBE"

	if _, err := ParseResponseMessage(values); err == nil {
		t.Error("모르는 status인데 파싱이 성공했습니다")
	}
}

func TestDestroyResponseRoundTrip(t *testing.T) {
	in := ResponseMessage{UUID: "9f1c-req-7", Action: ActionDestroy, Status: StatusDestroyed}

	out, err := ParseResponseMessage(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the response:\n got %+v\nwant %+v", *out, in)
	}
}
