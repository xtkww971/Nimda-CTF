package handler

import (
	"strings"

	"nimda-judge-provisioner/internal/queue"
)

// 작업 종류.
type TaskKind string

const (
	// 참가자가 접속할 인스턴스를 띄워야 하는 작업.
	TaskCreateInstance TaskKind = "CREATE_INSTANCE"
	// 첨부파일 다운로드 링크만 발급하면 되는 작업.
	TaskGetPresignedURL TaskKind = "GET_PRESIGNED_URL"
)

// 작업마다 필요한 필드가 달라서 타입이 갈리는데, 넘기는 쪽(Router)과 받는 쪽(분기)은
// 같은 자리를 지나야 한다. 그래서 공통 인터페이스를 둔다. any로 넘기지 않는 이유는,
// 여기 없는 타입이 실수로 흘러드는 걸 컴파일 시점에 막기 위해서다.
type TaskMessage interface {
	// 무슨 작업인지. 분기는 타입 스위치로 하지만, 로그와 오류 메시지에 쓸 이름이 필요하다.
	Kind() TaskKind
	// 이 요청의 uuid. 결과를 요청과 짝지을 때 쓴다.
	ID() string
}

// 인스턴스(컨테이너)가 필요한 문제 유형.
// todo 유형이 늘어나면 여기 추가. 웹 백엔드의 ChallengeCategory enum과 값을 맞춰야 한다.
var instanceCategories = map[string]bool{
	"WEB": true,
	"PWN": true,
}

// 문제 유형을 보고 무슨 작업인지 정한다.
// 대소문자와 공백은 무시한다.
func classify(category string) TaskKind {
	if instanceCategories[strings.ToUpper(strings.TrimSpace(category))] {
		return TaskCreateInstance
	}
	return TaskGetPresignedURL
}

// 스트림에서 올라온 요청을 작업 메시지로 옮긴다.
func newTaskMessage(msg *queue.Request) TaskMessage {
	switch classify(msg.ChallengeCategory) {
	case TaskCreateInstance:
		return &CreateInstanceMessage{
			UUID:              msg.UUID,
			ChallengeCode:     msg.ChallengeCode,
			ChallengeCategory: msg.ChallengeCategory,
			IsolationType:     msg.IsolationType,
			FlagType:          msg.FlagType,
		}
	default:
		return &GetPresignedURL{
			UUID:              msg.UUID,
			ChallengeCode:     msg.ChallengeCode,
			ChallengeCategory: msg.ChallengeCategory,
		}
	}
}

// 인스턴스 생성 작업에 필요한 것.
type CreateInstanceMessage struct {
	UUID              string
	ChallengeCode     string
	ChallengeCategory string
	IsolationType     string
	FlagType          string
}

func (m *CreateInstanceMessage) Kind() TaskKind { return TaskCreateInstance }
func (m *CreateInstanceMessage) ID() string     { return m.UUID }

// 첨부파일 링크 발급 작업에 필요한 것.
type GetPresignedURL struct {
	UUID              string
	ChallengeCode     string
	ChallengeCategory string // 로그용
}

func (m *GetPresignedURL) Kind() TaskKind { return TaskGetPresignedURL }
func (m *GetPresignedURL) ID() string     { return m.UUID }
