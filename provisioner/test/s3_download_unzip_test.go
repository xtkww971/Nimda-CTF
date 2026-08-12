package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nimda-judge-provisioner/internal/awsUtil"
	"nimda-judge-provisioner/internal/config"
)

// TestDownloadAndUnzipFromS3 exercises the real flow: download a challenge's
// zip from S3 by its code, unzip it into the configured work dir, and confirm
// the files actually land there.
//
//	code = web-basic-01  ->  challenges/web-basic-01/web-basic-01.zip
//
// Live-S3 integration test: skips when credentials or the object are absent.
func TestDownloadAndUnzipFromS3(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("설정 읽기 실패: %v", err)
	}
	if cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		t.Skip("AWS 자격증명이 없어 S3 통합 테스트를 건너뜁니다")
	}

	// 압축 해제 위치는 테스트용 임시 폴더로 (운영 기본값 /var/lib/ctf/build 대신).
	cfg.BuildWorkDir = t.TempDir()

	// 실제 키는 Spring 업로드 규칙과 같은 challenges/{code}/{code}.zip.
	const code = "web-basic-01"
	objectKey := "challenges/" + code + "/" + code + ".zip"

	store, err := awsUtil.NewS3Storage(ctx, cfg)
	if err != nil {
		t.Fatalf("S3 클라이언트 생성 실패: %v", err)
	}

	// 1) S3에서 zip 다운로드.
	zipData, err := store.GetFile(ctx, objectKey)
	if err != nil {
		if strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "StatusCode: 404") {
			t.Skipf("객체(%s)가 아직 없어 건너뜁니다", objectKey)
		}
		t.Fatalf("S3 다운로드 실패: %v", err)
	}
	if len(zipData) == 0 {
		t.Fatal("다운로드한 zip이 비어 있습니다")
	}
	t.Logf("다운로드 완료: %s (%d bytes)", objectKey, len(zipData))

	// 2) 압축 해제.
	dir, err := awsUtil.UnzipBytesToDir(zipData, &cfg)
	if err != nil {
		t.Fatalf("압축 해제 실패: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// 해제 폴더가 설정한 작업 폴더 아래에 만들어졌는지.
	if !strings.HasPrefix(dir, cfg.BuildWorkDir) {
		t.Errorf("해제 폴더 %q 가 작업 폴더 %q 아래가 아님", dir, cfg.BuildWorkDir)
	}

	// 3) 파일이 실제로 임시 폴더에 들어갔는지 확인.
	//    web-basic-01.zip 안에는 01-login-bypass/{app.py,Dockerfile}가 들어 있다.
	want := []string{
		filepath.Join("01-login-bypass", "app.py"),
		filepath.Join("01-login-bypass", "Dockerfile"),
	}
	for _, rel := range want {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			t.Errorf("기대한 파일이 없음: %s (%v)", rel, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("파일이 비어 있음: %s", rel)
		}
	}

	logTree(t, dir)
}

func logTree(t *testing.T, root string) {
	t.Helper()
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		t.Logf("  풀린 파일: %s (%d bytes)", rel, info.Size())
		return nil
	})
}
