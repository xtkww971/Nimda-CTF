핵심 기능

provisioner가 agent에게 인스턴스 생성 요청을 보내면
agent는 인스턴스를 생성 한 뒤 접속 정보를 provisioner에게 전달한다.
이후 provisioner가 웹사이트로 결과 메시지에 접속 정보를 싣는다.

type CreateRequest struct {
	UUID          string `json:"uuid"`
	ChallengeCode string `json:"challengeCode"` // 이미지·컨테이너 이름에 쓴다
	IsolationType string `json:"isolationType"` // SHARED | PER_USER
	FlagType      string `json:"flagType"`      // STATIC | DYNAMIC
	Flag          string `json:"flag,omitempty"`

	// 압축 해제 폴더 위치
	BuildContextPath string `json:"buildContextPath"`
}

여기서 UUID는 웹사이트에서 보낸 요청에 대한 ID를 의미하고
ctf 서버 내에서는 UUID - 인스턴스로 agent와 provisioner가 알고 있는 컨테이너 정보를 동기화 해야 한다.
이걸 이용해서 나중에 메모리 과사용 문제를 해결할 것이다.

지금은 간단하게 UUID - 인스턴스 id로 redis에 저장한다.
TTL은 이번 구현에서는 포함하지 않고 나중에 웹사이트에서 보내는 요청 메시지에 TTL을 추가해서
redis 객체 수명을 정할 것이다. - 아직은 구현되어 있지 않다.

agent와 provisioner간 통신에 사용하는 redis stream은 이미 존재하고 컨테이너 상태를 저장하는 것은 없다.
stream으로 하지 않고 uuid: prefix를 이용해서 컨테이너 id와 매핑해 저장한다.

_____
이전에 provisioner 내부에 있던 메시지를 하나로 모아서 간단하게 설정했는데
인스턴스에 대한 작업도 같은 방식으로 진행할 것이다.
agent 내부에서도 정보를 읽어서 작업을 판단하는 식으로 구현한다.

따라서 provisioner -> agent 요청 파이프라인 / agent -> provisioner 응답 파이프라인 / provisioner - agent 공통 redis(인스턴스 정보 저장)
위의 3가지가 필요하다. 

또한 요청 파이프라인에 들어가는 메시지를 CreateInstance같이 이름을 만들지 말고 추후 작업 확장성을 고려해 RequestMessage로 이름을 바꾼다.
결과 파이프라인에 들어가는 메시지도 Destroy처럼 하지 말고 ResponseMessage로 이름을 바꾸고 범용성을 높인다.
이후 들어있는 값을 토대로 해야 하는 작업을 나누는 로직을 추가할 예정

RequestMessage {
    UUID: string
    ACTION type <- 추가된 속성
        IsolationType:    isolationType,
		FlagType:         flagType,
		Flag:             optionalString(values, "flag"),
		BuildContextPath: buildContextPath,
} 이렇게 남긴다.

응답은 아직 어떤 식으로 할지 정하지 못했기에 이번 패치에서는 응답이 잘 가고 인스턴스가 잘 만들어지고 정보가 uuid: prefix로 관리되는지만 확인한다.