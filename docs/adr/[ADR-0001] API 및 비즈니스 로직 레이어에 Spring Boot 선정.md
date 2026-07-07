* **날짜:** 2026-07-07
* **작성자:** 이도현
* **상태:**  수락됨

## 1. Context (배경)
온라인 저지 시스템을 구축함에 있어 사용자 요청을 처리하고, 전체 시스템의 상태를 관리할 API 레이어의 기술 스택을 결정해야 함. 
고려 사항:
* 기존 웹 서비스 인프라와의 정합성.
* 대규모 채점 요청 처리 시 안정적인 보안 로직 및 최적화 구현.
* 채점 서버(Go Worker)와의 통신 편의성.

## 2. Decision (결정)
API 레이어 및 비즈니스 로직 관리를 위해 **Spring Boot**를 선정함.

## 3. Consequences (결과 및 영향)

### 🟢 장점
* **생태계 정합성:** 기존 운영 중인 웹 서비스가 Spring 기반으로 동작하고 있어, 인프라 관리 및 기술 스택 일관성을 유지할 수 있음.
* **보안 및 최적화 도구:** Java/Spring 생태계에서 제공하는 방대한 보안 라이브러리(Spring Security 등)와 비즈니스 로직 고도화 도구를 활용하여 시스템의 방어력을 높일 수 있음.
* **통신 효율성:** Spring의 강력한 Controller 기능을 활용하여 채점 서버(Go Worker) 및 클라이언트와 안정적인 REST API 기반 통신 체계를 구축하기에 매우 적합함.

### 🔴 단점 / 개선안
* **리소스 오버헤드:** Go 등 경량 언어에 비해 런타임 메모리 사용량이 많을 수 있음.
이를 최소화 하기 위해서 기존의 Controller-Service-Repository 구조가 아닌
Controller-Module 구조를 채택해 채점서버와 웹사이트 서버 통신만을 위해 스프링을 사용

### 참고자료
https://spring.io/guides/gs/gateway - 컨트롤러를 이용한 MSA 설계
https://www.baeldung.com/cs/anti-patterns
https://docs.spring.io/spring-security/reference/servlet/architecture.html - 스프링에서 제공하는 보안 설정 로직