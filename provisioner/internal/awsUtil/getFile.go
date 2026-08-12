package awsUtil

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	appConfig "nimda-judge-provisioner/internal/config"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (s *S3Storage) GetFile(ctx context.Context, objectKey string) ([]byte, error) {
	input := &s3.GetObjectInput{
		Bucket: &s.BucketName,
		Key:    &objectKey,
	}

	output, err := s.Client.GetObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("S3에서 파일 [%s] 가져오기 실패: %w", objectKey, err)
	}

	defer output.Body.Close()

	fileData, err := io.ReadAll(output.Body)

	if err != nil {
		return nil, fmt.Errorf("파일[%s] 읽어오기 실패 %w", objectKey, err)
	}

	return fileData, nil
}

// zip파일을 받아 압축을 해제한 뒤 폴더의 주소를 반환하는 함수
func UnzipBytesToDir(zipData []byte, cfg *appConfig.Config) (string, error) {
	// 1. 압축을 해제할 임시 폴더 생성.
	//    베이스 경로는 config.BuildWorkDir(운영에선 컨테이너 안의 쓰기 가능한 디스크 경로).
	//    요청마다 그 아래에 고유 하위 폴더를 만들어 동시 빌드가 서로 안 섞이고, 정리도
	//    이 폴더 단위로 지우면 되게 한다. BuildWorkDir이 ""면 OS 기본 임시 폴더를 쓴다.
	baseDir := cfg.BuildWorkDir
	if baseDir != "" {
		// MkdirTemp는 부모 폴더가 이미 있어야 하므로 없으면 먼저 만든다.
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			return "", fmt.Errorf("작업 베이스 폴더 생성 실패 [%s]: %w", baseDir, err)
		}
	}

	destDir, err := os.MkdirTemp(baseDir, "ctf-unzip-*")
	if err != nil {
		return "", fmt.Errorf("임시 폴더 생성 실패: %w", err)
	}

	// 2. []byte 데이터를 메모리 상에서 읽을 수 있도록 Reader로 변환
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return "", fmt.Errorf("Zip 데이터 읽기 실패: %w", err)
	}

	// 3. Zip 파일 내부의 파일들을 순회하며 압축 해제
	for _, f := range zipReader.File {
		// 경로 조작 공격(ZipSlip) 방지: 파일이 목적지 폴더를 벗어나지 못하도록 검증
		fpath := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return "", fmt.Errorf("잘못된 파일 경로 감지됨 (ZipSlip 위험): %s", fpath)
		}

		// 폴더인 경우 폴더만 생성하고 다음으로 패스
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		// 파일이 들어갈 상위 폴더가 없다면 미리 생성
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return "", fmt.Errorf("파일 디렉토리 생성 실패: %w", err)
		}

		// 압축 파일 내의 개별 파일 열기
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("압축 파일 내부 [%s] 열기 실패: %w", f.Name, err)
		}

		// 압축을 해제하여 저장할 실제 파일 생성
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close() // 에러 발생 시 열어둔 Reader 닫기
			return "", fmt.Errorf("출력 파일 생성 실패: %w", err)
		}

		// 데이터 복사 (압축 풀기)
		_, err = io.Copy(outFile, rc)

		// 리소스 반환
		outFile.Close()
		rc.Close()

		if err != nil {
			return "", fmt.Errorf("파일 [%s] 압축 해제 중 에러: %w", f.Name, err)
		}
	}

	// 4. 모든 파일이 정상적으로 풀린 임시 폴더의 경로 반환
	return destDir, nil
}
