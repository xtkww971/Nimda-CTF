// Package isolate wraps the isolate(1) sandbox CLI (https://github.com/ioi/isolate)
// so the judge package can run untrusted code without shelling out directly.
package isolate

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// isolate 바이너리 경로. PATH에 있으면 "isolate"만으로 충분하지만,
// 채점 서버에서는 절대경로로 고정해두는 걸 추천합니다.
var BinaryPath = "isolate"

// Box는 isolate 샌드박스 한 칸(box)을 표현합니다.
// isolate는 --box-id로 여러 개의 격리 공간을 동시에 운용할 수 있습니다.
type Box struct {
	ID   int    // isolate --box-id 값
	Path string // isolate --init이 반환하는 실제 박스 디렉토리 (보통 .../box)

	// UseCgroups는 --cg 옵션을 사용할지 여부입니다.
	// cgroup을 쓰면 --cg-mem, --cg-timing 등으로 더 정확한 메모리 측정이 가능합니다.
	// isolate.conf에 cgroup이 활성화되어 있어야 합니다.
	UseCgroups bool
}

// NewBox는 Box 구조체만 생성합니다. 실제 샌드박스 준비는 Init()에서 합니다.
func NewBox(id int, useCgroups bool) *Box {
	return &Box{ID: id, UseCgroups: useCgroups}
}

// Init은 `isolate --box-id=N --init`을 실행해 샌드박스 디렉토리를 만듭니다.
// 이미 같은 box-id로 초기화되어 있다면 isolate가 알아서 정리 후 재생성합니다.
func (b *Box) Init() error {
	args := b.baseArgs()
	args = append(args, "--init")

	out, err := runIsolateCLI(args...)
	if err != nil {
		return fmt.Errorf("isolate init 실패 (box-id=%d): %w (output: %s)", b.ID, err, out)
	}

	outerPath := strings.TrimSpace(out)
	if outerPath == "" {
		return fmt.Errorf("isolate init: box 경로를 받아오지 못했습니다 (box-id=%d)", b.ID)
	}
	// isolate --init은 박스 "바깥" 경로(예: /var/local/lib/isolate/0)를 출력합니다.
	// 실제 프로그램이 chroot/chdir되는 작업 디렉토리는 그 아래 "box" 서브디렉토리
	// (/var/local/lib/isolate/0/box)이므로, 소스/입출력 파일은 여기에 둬야 합니다.
	b.Path = filepath.Join(outerPath, "box")
	return nil
}

// Cleanup은 `isolate --box-id=N --cleanup`을 실행해 샌드박스를 완전히 삭제합니다.
// 채점이 끝나면(성공/실패 무관) 반드시 호출해야 합니다. defer로 사용하세요.
func (b *Box) Cleanup() error {
	args := b.baseArgs()
	args = append(args, "--cleanup")

	out, err := runIsolateCLI(args...)
	if err != nil {
		return fmt.Errorf("isolate cleanup 실패 (box-id=%d): %w (output: %s)", b.ID, err, out)
	}
	return nil
}

func (b *Box) baseArgs() []string {
	args := []string{fmt.Sprintf("--box-id=%d", b.ID)}
	if b.UseCgroups {
		args = append(args, "--cg")
	}
	return args
}

func runIsolateCLI(args ...string) (string, error) {
	cmd := exec.Command(BinaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(stdout.String() + " " + stderr.String())
		return combined, err
	}
	return stdout.String(), nil
}

// ---------------------------------------------------------------------
// BoxPool: 동시 채점 워커 수만큼 box-id를 미리 할당해두고 재사용합니다.
// isolate는 box-id별로 리눅스 사용자를 하나씩 예약해서 쓰기 때문에
// (isolate.conf의 first_uid/first_gid 범위), 무한정 늘릴 수 없습니다.
// ---------------------------------------------------------------------

type BoxPool struct {
	ids chan int
	mu  sync.Mutex
}

// NewBoxPool은 [0, n) 범위의 box-id 풀을 만듭니다.
// n은 보통 워커 고루틴 개수(동시 채점 개수)와 맞춥니다.
func NewBoxPool(n int) *BoxPool {
	ids := make(chan int, n)
	for i := 0; i < n; i++ {
		ids <- i
	}
	return &BoxPool{ids: ids}
}

// Acquire는 사용 가능한 box-id 하나를 꺼내 Box를 초기화(Init)까지 완료해서 돌려줍니다.
// 풀이 꽉 차 있으면(모두 사용 중) 반환될 때까지 블록됩니다.
func (p *BoxPool) Acquire(useCgroups bool) (*Box, error) {
	id := <-p.ids
	box := NewBox(id, useCgroups)
	if err := box.Init(); err != nil {
		p.ids <- id // 실패했으면 id 반납
		return nil, err
	}
	return box, nil
}

// Release는 박스를 정리(Cleanup)하고 box-id를 풀에 반납합니다.
func (p *BoxPool) Release(box *Box) error {
	err := box.Cleanup()
	p.ids <- box.ID
	return err
}

// IDToString은 로깅용 헬퍼입니다.
func IDToString(id int) string {
	return strconv.Itoa(id)
}
