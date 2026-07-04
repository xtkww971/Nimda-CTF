# Nimda Judge Server

![Spring Boot](https://img.shields.io/badge/Spring%20Boot-6DB33F?style=flat-square&logo=Spring%20Boot&logoColor=white)
![Go](https://img.shields.io/badge/Go-00ADD8?style=flat-square&logo=Go&logoColor=white)
![RabbitMQ](https://img.shields.io/badge/RabbitMQ-FF6600?style=flat-square&logo=RabbitMQ&logoColor=white)
![MySQL](https://img.shields.io/badge/MySQL-4479A1?style=flat-square&logo=MySQL&logoColor=white)
![AWS S3](https://img.shields.io/badge/Amazon%20S3-569A31?style=flat-square&logo=Amazon%20S3&logoColor=white)
![Linux](https://img.shields.io/badge/Linux-FCC624?style=flat-square&logo=linux&logoColor=black)

## 프로젝트 소개
Nimda Judge Server은 사용자가 제출한 소스 코드를 안전하게 격리된 환경에서 컴파일하고 실행하여, 제한 시간 및 메모리 기준에 따라 정답 여부를 판정하는 **마이크로서비스 기반의 알고리즘 채점 서버**입니다.

대규모 코딩 테스트 환경에서 발생할 수 있는 트래픽 증가와 악의적인 코드(해킹, 자원 고갈로부터 시스템을 보호하기 위해 비동기 메시지 큐와 OS 커널 레벨의 샌드박스를 도입하여 설계했습니다.

---

## 시스템 아키텍처 (System Architecture)

<img src="./img/System Architecture.png">