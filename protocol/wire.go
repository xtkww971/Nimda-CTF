package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Wire framing.
//
// Every message on the socket is length-prefixed:
//
//	[ 4 bytes, big-endian uint32 = N ][ N bytes payload ]
//
// Control frames (RequestFrame / ResponseFrame) carry JSON as the payload.
// The Create build context is sent as one more length-prefixed frame right
// after the Create request, with the raw tar bytes as its payload.
//
// Length prefixing (rather than newline- or JSON-delimited) is used because
// the build context is arbitrary binary and must not be re-interpreted.

const (
	// MaxControlFrameSize caps a JSON control frame. Control frames are small;
	// this only guards against a corrupt/huge length prefix.
	MaxControlFrameSize = 1 << 20 // 1 MiB

	// MaxBlobSize caps a build-context blob. Challenge build contexts are small
	// (a Dockerfile plus a little code); this bounds memory and rejects abuse.
	MaxBlobSize = 512 << 20 // 512 MiB
)

// WriteFrame marshals v to JSON and writes it as one length-prefixed frame.
// Used for both RequestFrame and ResponseFrame.
func WriteFrame(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("프레임 직렬화 실패: %w", err)
	}
	if len(payload) > MaxControlFrameSize {
		return fmt.Errorf("컨트롤 프레임이 너무 큽니다: %d bytes", len(payload))
	}
	return writeLengthPrefixed(w, payload)
}

// ReadFrame reads one length-prefixed frame and unmarshals its JSON into v.
func ReadFrame(r io.Reader, v any) error {
	payload, err := readLengthPrefixed(r, MaxControlFrameSize)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return fmt.Errorf("프레임 역직렬화 실패: %w", err)
	}
	return nil
}

// WriteBlob writes raw bytes (the build context tar) as one length-prefixed
// frame. Callers stream a tar of the unzipped challenge files here.
func WriteBlob(w io.Writer, data []byte) error {
	if len(data) > MaxBlobSize {
		return fmt.Errorf("빌드 컨텍스트가 너무 큽니다: %d bytes (최대 %d)", len(data), MaxBlobSize)
	}
	return writeLengthPrefixed(w, data)
}

// ReadBlob reads one length-prefixed binary frame (the build context).
func ReadBlob(r io.Reader) ([]byte, error) {
	return readLengthPrefixed(r, MaxBlobSize)
}

func writeLengthPrefixed(w io.Writer, payload []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("길이 헤더 쓰기 실패: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("페이로드 쓰기 실패: %w", err)
	}
	return nil
}

func readLengthPrefixed(r io.Reader, max int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err // includes io.EOF when the peer closed cleanly
	}
	n := binary.BigEndian.Uint32(header[:])
	if int64(n) > int64(max) {
		return nil, fmt.Errorf("프레임 길이 %d 가 상한 %d 를 넘습니다", n, max)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("페이로드 읽기 실패: %w", err)
	}
	return payload, nil
}
