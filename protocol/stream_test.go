package protocol

import "testing"

// Redis Stream은 평평한 문자열 맵만 싣는다. 여기서 확인하려는 건 "한 바퀴 돌려도 값이
// 그대로냐" 하나다 — 조율자가 실어 보낸 걸 에이전트가 같은 값으로 읽어야 한다.

func TestCreateRequestRoundTrip(t *testing.T) {
	in := CreateRequest{
		RequestID:        "req-1",
		ChallengeCode:    "web-basic-01",
		UserID:           42,
		IsolationType:    "PER_USER",
		FlagType:         "DYNAMIC",
		Flag:             "FLAG{abc}",
		BuildContextPath: "/var/lib/ctf/build/req-1",
	}

	out, err := ParseCreateRequest(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the request:\n got %+v\nwant %+v", *out, in)
	}
}

// STATIC 문제는 flag를 안 싣는다. 빈 값이 "FLAG=" 로 주입되면 안 되므로 빠진 채로 와야 한다.
func TestCreateRequestOmitsEmptyFlag(t *testing.T) {
	in := CreateRequest{
		RequestID:        "req-2",
		ChallengeCode:    "web-basic-01",
		UserID:           1,
		IsolationType:    "SHARED",
		FlagType:         "STATIC",
		BuildContextPath: "/var/lib/ctf/build/req-2",
	}

	values := in.ToStreamValues()
	if _, ok := values["flag"]; ok {
		t.Error("flag가 비었는데도 스트림에 실렸습니다")
	}
	out, err := ParseCreateRequest(values)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Flag != "" {
		t.Errorf("flag = %q, want empty", out.Flag)
	}
}

// 에이전트가 빌드할 경로가 없으면 할 수 있는 일이 없다. 조용히 넘어가면 빈 컨텍스트를
// 빌드하려 들기 때문에 파싱에서 걸러야 한다.
func TestCreateRequestRejectsMissingFields(t *testing.T) {
	base := CreateRequest{
		RequestID:        "req-3",
		ChallengeCode:    "web-basic-01",
		UserID:           1,
		IsolationType:    "SHARED",
		FlagType:         "STATIC",
		BuildContextPath: "/var/lib/ctf/build/req-3",
	}.ToStreamValues()

	for _, key := range []string{"requestId", "challengeCode", "userId", "isolationType", "flagType", "buildContextPath"} {
		values := map[string]interface{}{}
		for k, v := range base {
			values[k] = v
		}
		delete(values, key)

		if _, err := ParseCreateRequest(values); err == nil {
			t.Errorf("%q가 빠졌는데 파싱이 성공했습니다", key)
		}
	}
}

func TestCreateResultRoundTrip(t *testing.T) {
	in := CreateResult{
		RequestID:  "req-1",
		Status:     StatusReady,
		InstanceID: "c0ffee",
		Host:       "chal.nimda.kr",
		Port:       32768,
		ExpiresAt:  "2026-09-09T12:00:00Z",
	}

	out, err := ParseCreateResult(in.ToStreamValues())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the result:\n got %+v\nwant %+v", *out, in)
	}
}

// 실패 결과는 host/port/expiresAt가 전부 빈 채로 온다. 0번 포트로 읽히면 안 된다.
func TestCreateResultFailedHasNoHostPort(t *testing.T) {
	in := CreateResult{
		RequestID: "req-4",
		Status:    StatusFailed,
		Message:   "이미지 빌드 실패",
	}

	values := in.ToStreamValues()
	for _, key := range []string{"host", "port", "expiresAt", "instanceId"} {
		if _, ok := values[key]; ok {
			t.Errorf("실패 결과에 %q가 실렸습니다", key)
		}
	}

	out, err := ParseCreateResult(values)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Port != 0 || out.Host != "" {
		t.Errorf("실패 결과에 host/port가 채워졌습니다: %+v", *out)
	}
	if out.Message != in.Message {
		t.Errorf("message = %q, want %q", out.Message, in.Message)
	}
}

// 포트가 숫자가 아니면 접속 못 하는 인스턴스를 READY로 넘기게 된다. 조용히 0으로 두지 않는다.
func TestCreateResultRejectsBadPort(t *testing.T) {
	values := CreateResult{RequestID: "req-5", Status: StatusReady, Host: "h", Port: 1}.ToStreamValues()
	values["port"] = "not-a-number"

	if _, err := ParseCreateResult(values); err == nil {
		t.Error("포트가 숫자가 아닌데 파싱이 성공했습니다")
	}
}

func TestCreateResultRejectsUnknownStatus(t *testing.T) {
	values := CreateResult{RequestID: "req-6", Status: StatusReady}.ToStreamValues()
	values["status"] = "MAYBE"

	if _, err := ParseCreateResult(values); err == nil {
		t.Error("모르는 status인데 파싱이 성공했습니다")
	}
}

func TestDestroyRoundTrip(t *testing.T) {
	req := DestroyRequest{
		RequestID:     "req-7",
		InstanceID:    "ctf-web-basic-01-u42",
		ChallengeCode: "web-basic-01",
		UserID:        42,
	}
	gotReq, err := ParseDestroyRequest(req.ToStreamValues())
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if *gotReq != req {
		t.Errorf("round trip changed the request:\n got %+v\nwant %+v", *gotReq, req)
	}

	res := DestroyResult{RequestID: "req-7", Status: DestroyDone}
	gotRes, err := ParseDestroyResult(res.ToStreamValues())
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if *gotRes != res {
		t.Errorf("round trip changed the result:\n got %+v\nwant %+v", *gotRes, res)
	}
}
