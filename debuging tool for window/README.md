# 🐋 Nimda 프로젝트 로컬 도커 가동 가이드 (Windows 전용)

본 가동 세트는 메인 웹 백엔드 서버와 채점 서버를 독립적으로 분리하여 제어할 수 있는 로컬 개발 환경 인프라입니다. 각 프로젝트의 Git 저장소(Repository) 환경과 무관하게 독립적으로 작동하며, 개인 PC 환경에 맞춘 유연한 설정이 가능합니다.

> ⚠️ **OS 제한 사항:** 본 패키지에 포함된 자동화 스크립트(`.bat`) 및 Docker 인바운드 통신 설정은 **Windows 환경 전용**으로 설계되었습니다. (Mac/Linux 미지원)

---

## 📂 1. 준비 사항 (Dockerfile 위치 확인)

이 시스템이 정상적으로 빌드되려면, 메인 웹 레포지토리와 채점 서버 레포지토리 내부에 각각 `Dockerfile`이 존재해야 합니다. 실행 전 아래 경로에 파일이 있는지 꼭 확인해 주세요.

* **웹 백엔드:** `.../backend-spring/Dockerfile`
* **채점 서버:** `.../webController/Dockerfile`

---

## ⚙️ 2. 초기 설정 가이드 (.env 입력값)

각 서비스 폴더(`web-service`, `judge-service`) 내부의 `.env` 파일을 메모장으로 열어 본인 로컬 PC 환경에 맞게 값을 수정해야 합니다.

### ① `web-service/.env` 설정 항목
* `BACKEND_PATH`: **[가장 중요]** 메인 웹 백엔드의 `Dockerfile`이 위치한 **폴더의 절대 경로**를 입력합니다.
  * *입력 예시:* `C:\Users\Username\Git\Nimda\NimdaConBackEnd\backend-spring`
* `SPRING_DATASOURCE_URL`: 도커 컨테이너에서 내 PC(로컬)의 MySQL에 접속하기 위한 주소입니다. 웬만하면 기본값인 `host.docker.internal`을 유지하되, 포트나 DB명이 다르다면 수정합니다.
* `SPRING_DATASOURCE_PASSWORD`: 본인 로컬 MySQL의 비밀번호를 적어줍니다.
* `JWT_SECRET`: JWT 토큰 검증용 시크릿 키입니다. (팀 공용 키 사용 권장)

### ② `judge-service/.env` 설정 항목
* `JUDGE_PATH`: 채점 서버의 `Dockerfile`이 위치한 **폴더의 절대 경로**를 입력합니다.
  * *입력 예시:* `C:\Users\Username\Git\Judge Server\webController`

> 💡 **PATH 입력 시 주의사항:** 경로 뒤에 슬래시(`/`)나 역슬래시(`\`)를 붙이지 말고 폴더 이름까지만 깔끔하게 적어주세요. (공백이 포함된 경로명도 정상 인식합니다.)

---

## 🚀 3. 실행 및 종료 순서 (필독)

두 서비스가 네트워크망과 메시지 큐(Redis)를 공유하므로, **반드시 아래 실행 순서를 엄격히 준수**해야 에러가 나지 않습니다.

### 🟢 서버 켤 때 (시작 순서)
1. **Docker Desktop** 프로그램을 실행합니다. (왼쪽 아래 고래 아이콘이 **초록불**이 될 때까지 대기)
2. `web-service/` 폴더로 이동하여 **`run-web.bat`**을 더블클릭합니다.
   * *이때 Redis 큐와 웹 백엔드 컨테이너가 먼저 구동되며 공용 네트워크망이 개설됩니다.*
3. 웹 서버가 정상 구동된 것을 확인한 후, `judge-service/` 폴더로 이동하여 **`run-judge.bat`**을 더블클릭합니다.

### 🔴 서버 끌 때 (종료 순서)
* 꺼지는 순서는 크게 상관없으나 찌꺼기 없는 깔끔한 정리를 위해 아래 순서를 권장합니다.
1. `judge-service/` ➡️ **`stop-judge.bat`** 실행
2. `web-service/` ➡️ **`stop-web.bat`** 실행

---

## 🛠️ 4. 개발 시 주의 사항

* **채점 서버 코드만 수정했을 때:** 웹 서버는 그대로 켜둔 채, `judge-service/` 폴더 안에서 `stop-judge.bat` ➡️ `run-judge.bat`만 해주시면 웹 서버 중단 없이 채점 엔진만 3초 만에 초고속 리로드됩니다.
* **인텔리제이(ID_EA) 로컬 디버깅 할 때:** 본 설정 도입으로 인해 인텔리제이에서 코드를 그냥 런/디버그 하시면 자동으로 프로필 없이 `localhost` 기반으로 작동합니다. 디버깅 시에는 도커 컴포즈를 켜실 필요가 없습니다. (단, Redis 메시지 큐 통신이 필요하다면 `web-service/run-web.bat`을 켜서 도커 Redis만 띄워둔 채 인텔리제이를 구동하시면 됩니다.)

---

## 🚨 5. 주요 트러블슈팅 및 주의사항

1. **`failed to connect to the docker API` 에러 발생 시**
   * Docker Desktop 프로그램이 꺼져 있거나 아직 로딩 중일 때 배치 파일을 누르면 발생합니다. 프로그램 창을 열어 초록불 상태인지 확인 후 다시 배치 파일을 켜세요.
2. **`network nimda-network declared as external, but could not be found` 에러 발생 시**
   * 웹 서버(`run-web.bat`)를 먼저 켜지 않고 채점 서버(`run-judge.bat`)를 먼저 켰을 때 발생하는 도커 고유 에러입니다. 반드시 웹 서버를 먼저 구동해 주세요.
3. **데이터 소스 연결 실패 (DB Connection Fail)**
   * 내 PC 로컬에 MySQL 서비스가 켜져 있는지 확인하고, `.env`에 적은 비밀번호가 로컬 DB 비밀번호와 일치하는지 다시 한번 점검하세요.
4. **포트 충돌**
   * `intellj`와 `docker`를 동시에 실행하면 포트를 명시적으로 분리하지 않은 경우 포트 충돌 오류가 발생할 수 있습니다. 따라서 api 테스트를 하고 싶다면 컨테이너와 intellj가 동시에 실행되면 안됩니다.