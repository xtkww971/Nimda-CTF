package archive

import (
	"archive/zip"
	"bytes"
	appConfig "nimda-judge-provisioner/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip builds an in-memory zip from name->content entries.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestUnzipBytesToDirUsesConfiguredBaseDir(t *testing.T) {
	base := t.TempDir()
	cfg := &appConfig.Config{BuildWorkDir: base}

	zipData := makeZip(t, map[string]string{
		"Dockerfile": "FROM scratch\n",
		"app/app.py": "print('hi')\n",
	})

	dir, err := UnzipBytesToDir(zipData, cfg)
	if err != nil {
		t.Fatalf("unzip: %v", err)
	}

	// 압축 해제 폴더가 설정한 베이스 경로 아래에 만들어져야 한다.
	if !strings.HasPrefix(dir, base) {
		t.Errorf("압축 해제 폴더 %q 가 베이스 %q 아래가 아님", dir, base)
	}

	// 파일이 실제로 풀렸는지 확인.
	if b, err := os.ReadFile(filepath.Join(dir, "Dockerfile")); err != nil || !strings.Contains(string(b), "FROM scratch") {
		t.Errorf("Dockerfile 압축 해제 실패: %v, %q", err, string(b))
	}
	if _, err := os.Stat(filepath.Join(dir, "app", "app.py")); err != nil {
		t.Errorf("중첩 파일 압축 해제 실패: %v", err)
	}
}

func TestUnzipBytesToDirCreatesMissingBaseDir(t *testing.T) {
	// 아직 존재하지 않는 베이스 경로를 줘도 알아서 만들어야 한다.
	base := filepath.Join(t.TempDir(), "not", "yet", "there")
	cfg := &appConfig.Config{BuildWorkDir: base}

	dir, err := UnzipBytesToDir(makeZip(t, map[string]string{"a.txt": "x"}), cfg)
	if err != nil {
		t.Fatalf("unzip: %v", err)
	}
	if !strings.HasPrefix(dir, base) {
		t.Errorf("압축 해제 폴더 %q 가 베이스 %q 아래가 아님", dir, base)
	}
}

func TestUnzipBytesToDirRejectsZipSlip(t *testing.T) {
	cfg := &appConfig.Config{BuildWorkDir: t.TempDir()}

	// 상위 경로로 탈출하려는 엔트리는 거부되어야 한다.
	zipData := makeZip(t, map[string]string{"../evil.txt": "pwn"})

	if _, err := UnzipBytesToDir(zipData, cfg); err == nil {
		t.Fatal("ZipSlip 경로를 거부하지 않았습니다")
	}
}
