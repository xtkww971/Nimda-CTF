package queue

import "strconv"

// 웹 백엔드 ↔ 조율자 메시지.
//
// 예전에는 인스턴스 생성과 첨부파일 다운로드가 각자 스트림 한 쌍씩을 가졌고, 메시지
// 구조도 따로였다. 지금은 요청 스트림 하나, 결과 스트림 하나다.
//
// 그래서 "무엇을 해달라"를 메시지에 실을 필요가 없어졌다. 예전 action 필드는 사실 아무도
// 안 봤다 — 분기는 어느 스트림으로 들어왔느냐로 했기 때문이다. 이제 판단은 전부 조율자가
// 하고, 기준은 ChallengeCategory 하나다(router.go).
//
// 식별자도 UUID 하나로 줄었다. 웹이 사용자마다 발급하고 UUID ↔ 사용자 매핑을 자기 쪽
// redis에서 관리하므로, CTF 서버는 요청한 사람이 누구인지 알 필요가 없다. 남의 인스턴스에
// 접근하는 것을 막는 것도 이 UUID를 모르면 주소를 못 만드는 것으로 처리한다.

// 웹 백엔드가 보내는 요청. 종류가 하나뿐이라 이 타입 하나가 전부다.
type Request struct {
	UUID              string
	ChallengeCode     string // S3 객체 경로에도 쓰인다
	ChallengeCategory string // 무엇을 할지 정하는 유일한 기준
	IsolationType     string // "SHARED" | "PER_USER"
	FlagType          string // "STATIC" | "DYNAMIC"
}

// redis로 받은 raw data를 Request로 변환.
//
// 다섯 필드 전부 필수다. 웹은 Challenge 엔티티에서 값을 채워 보내므로 빠질 이유가 없고,
// 빠졌다면 웹 쪽 버그다. 조용히 넘어가면 isolationType 없이 인스턴스를 띄우려 드는 식으로
// 더 나중에, 더 알아보기 어려운 모양으로 터진다.
func ParseRequest(values map[string]interface{}) (*Request, error) {
	uuid, err := requiredString(values, "uuid")
	if err != nil {
		return nil, err
	}
	challengeCode, err := requiredString(values, "challengeCode")
	if err != nil {
		return nil, err
	}
	challengeCategory, err := requiredString(values, "challengeCategory")
	if err != nil {
		return nil, err
	}
	isolationType, err := requiredString(values, "isolationType")
	if err != nil {
		return nil, err
	}
	flagType, err := requiredString(values, "flagType")
	if err != nil {
		return nil, err
	}

	return &Request{
		UUID:              uuid,
		ChallengeCode:     challengeCode,
		ChallengeCategory: challengeCategory,
		IsolationType:     isolationType,
		FlagType:          flagType,
	}, nil
}

// 처리 결과 상태.
const (
	StatusReady  = "READY"
	StatusFailed = "FAILED"
)

// 조율자가 돌려주는 결과. 인스턴스든 다운로드든 이 타입 하나로 나간다.
//
// Host/Port는 인스턴스일 때, DownloadURL은 첨부파일일 때만 찬다. 받는 쪽은 어느 쪽이
// 들어있는지로 구분한다 — 요청에 action이 없으니 결과에도 없다.
type Result struct {
	UUID        string
	Status      string // StatusReady | StatusFailed
	Message     string // 실패 사유
	Host        string // 인스턴스 READY일 때
	Port        int    // 인스턴스 READY일 때
	ExpiresAt   string // RFC3339. 인스턴스는 PER_USER일 때, 다운로드는 링크 만료 시각
	DownloadURL string // 첨부파일 READY일 때
}

// 결과를 redis stream에 넣는 함수.
//
// 빈 값은 키째로 뺀다. port가 "0"으로 실리면 받는 쪽이 0번 포트로 읽고, downloadUrl이
// 빈 문자열로 실리면 링크가 있는 줄 안다. "키가 없다 = 값이 없다"로 맞춘다.
func (r Result) ToStreamValues() map[string]interface{} {
	values := map[string]interface{}{
		"uuid":   r.UUID,
		"status": r.Status,
	}
	if r.Message != "" {
		values["message"] = r.Message
	}
	if r.Host != "" {
		values["host"] = r.Host
	}
	if r.Port != 0 {
		values["port"] = strconv.Itoa(r.Port)
	}
	if r.ExpiresAt != "" {
		values["expiresAt"] = r.ExpiresAt
	}
	if r.DownloadURL != "" {
		values["downloadUrl"] = r.DownloadURL
	}
	return values
}
