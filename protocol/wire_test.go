package protocol

import (
	"bytes"
	"testing"
)

func TestCreateFrameAndBlobRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	// Provisioner side: write a create request, then the build context blob.
	req := RequestFrame{
		Type: TypeCreate,
		Create: &CreateRequest{
			RequestID:     "req-1",
			ChallengeCode: "web-basic-01",
			UserID:        42,
			IsolationType: "PER_USER",
			FlagType:      "DYNAMIC",
			Flag:          "nimda{per_user_flag}",
		},
	}
	if err := WriteFrame(&buf, req); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	buildCtx := []byte("PK\x03\x04 pretend-this-is-a-tar")
	if err := WriteBlob(&buf, buildCtx); err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}

	// Agent side: read the request, then the blob, in the same order.
	var got RequestFrame
	if err := ReadFrame(&buf, &got); err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Type != TypeCreate || got.Create == nil {
		t.Fatalf("wrong frame decoded: %+v", got)
	}
	if got.Create.ChallengeCode != "web-basic-01" || got.Create.UserID != 42 {
		t.Errorf("create payload wrong: %+v", got.Create)
	}
	if got.Create.Flag != "nimda{per_user_flag}" {
		t.Errorf("flag not carried: %q", got.Create.Flag)
	}

	gotBlob, err := ReadBlob(&buf)
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if !bytes.Equal(gotBlob, buildCtx) {
		t.Errorf("build context corrupted: %q", gotBlob)
	}
}

func TestResultFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer

	resp := ResponseFrame{
		Type: TypeCreate,
		Create: &CreateResult{
			RequestID:  "req-1",
			Status:     StatusReady,
			InstanceID: "ctr-abc123",
			Host:       "chal.nimda.kr",
			Port:       31337,
			ExpiresAt:  "2026-08-17T12:00:00Z",
		},
	}
	if err := WriteFrame(&buf, resp); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	var got ResponseFrame
	if err := ReadFrame(&buf, &got); err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Create == nil || got.Create.Status != StatusReady {
		t.Fatalf("result wrong: %+v", got)
	}
	if got.Create.InstanceID != "ctr-abc123" || got.Create.Port != 31337 {
		t.Errorf("result fields wrong: %+v", got.Create)
	}
}

func TestErrorResponse(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, ResponseFrame{Type: TypeCreate, Error: "build failed"}); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	var got ResponseFrame
	if err := ReadFrame(&buf, &got); err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Error != "build failed" || got.Create != nil {
		t.Errorf("error frame wrong: %+v", got)
	}
}

func TestBlobRejectsOversizeLength(t *testing.T) {
	// A corrupt 4-byte length prefix claiming a huge blob must be rejected,
	// not eagerly allocated.
	oversize := []byte{0xFF, 0xFF, 0xFF, 0xFF} // ~4 GiB
	if _, err := ReadBlob(bytes.NewReader(oversize)); err == nil {
		t.Fatal("oversize length prefix should be rejected")
	}
}
