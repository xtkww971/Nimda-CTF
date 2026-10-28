// docker cli 모듈
package runtime

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	units "github.com/docker/go-units"
)

// 인스턴스를 어떻게 띄울지 정하는 옵션.
type Options struct {
	// 컨테이너 런타임 이름. 비우면 도커 기본값. 운영에선 "runsc"(gVisor, ADR-0008),
	// gVisor 없는 개발 호스트에선 비워둔다.
	Runtime string

	// 인스턴스마다 거는 메모리/프로세스 상한
	MemoryLimit string
	PidsLimit   int
}

// 접속 방식에 따라 달라지는 실행 옵션. 부르는 쪽(handler)이 채워 넘긴다.
// zero value면 지금까지의 HTTP(web) 동작과 같다 — loopback 임의 포트, 기본 네트워크.
type RunSpec struct {
	// host 포트를 열 IP. 비면 "127.0.0.1"(web). pwn은 "0.0.0.0"처럼 밖에서 닿는 IP.
	BindIP string
	// 열 host 포트. 0이면 데몬이 고른다(web). pwn은 포트 풀에서 받은 값.
	HostPort int
	// 붙일 네트워크. 비면 기본 bridge(web). pwn은 "ctf-pwn".
	Network string
}

// 재시작 시 포트 풀을 다시 계산할 때 쓴다(이슈 #29의 InitPortPool).
// nimda.ctf.port 라벨이 붙은 컨테이너의 포트를 모은다.
const labelPort = labelMarker + ".port"

type Docker struct {
	cli  *client.Client
	opts Options
}

func NewDocker(opts Options) (*Docker, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("도커 데몬에 연결하지 못했습니다: %w", err)
	}
	return &Docker{cli: cli, opts: opts}, nil
}

func (d *Docker) Close() error { return d.cli.Close() }

// provisioner가 이미 압축을 풀어둔 sourceDir에서 Dockerfile을 찾아 imageTag로 이미지를 빌드
func (d *Docker) BuildImageFromDir(ctx context.Context, sourceDir, imageTag string) error {
	contextDir, err := findDockerfileDir(sourceDir)
	if err != nil {
		return err
	}

	tarStream, err := tarDir(contextDir)
	if err != nil {
		return fmt.Errorf("빌드 컨텍스트 tar 생성 실패: %w", err)
	}
	defer tarStream.Close()

	resp, err := d.cli.ImageBuild(ctx, tarStream, build.ImageBuildOptions{
		Tags:       []string{imageTag},
		Dockerfile: "Dockerfile",
		Remove:     true, // 중간 컨테이너 정리
	})
	if err != nil {
		return fmt.Errorf("이미지 빌드 요청 실패: %w", err)
	}
	defer resp.Body.Close()

	// 빌드 실패는 HTTP 오류가 아니라 스트림 안의 errorDetail로 온다.
	return drainBuildOutput(resp.Body)
}

// 인스턴스 컨테이너마다 붙이는 라벨.
const labelMarker = "nimda.ctf"

func (d *Docker) Run(ctx context.Context, imageTag, name string, env, labels map[string]string, spec RunSpec) (instanceID string, hostPort int, err error) {
	containerPort, err := d.exposedPort(ctx, imageTag)
	if err != nil {
		return "", 0, err
	}

	// 같은 이름의 컨테이너가 있으면 지우고 다시 만든다.
	// PER_USER만 삭제하고 SHARED의 경우 삭제하지 않는다.
	_ = d.Remove(ctx, name)

	// 접속 방식별 값. zero value면 지금까지의 web 동작이다.
	bindIP := spec.BindIP
	if bindIP == "" {
		bindIP = "127.0.0.1"
	}
	binding := nat.PortBinding{HostIP: bindIP} // HostPort 빈 값이면 데몬이 고른다
	if spec.HostPort != 0 {
		binding.HostPort = strconv.Itoa(spec.HostPort)
	}

	// TCP는 포트가 미리 정해져 있으므로, 재시작 때 풀 재계산이 읽을 수 있게 라벨에 박는다.
	runLabels := labels
	if spec.HostPort != 0 {
		runLabels = make(map[string]string, len(labels)+1)
		for k, v := range labels {
			runLabels[k] = v
		}
		runLabels[labelPort] = strconv.Itoa(spec.HostPort)
	}

	cfg := &container.Config{
		Image:        imageTag,
		Env:          envList(env),
		Labels:       containerLabels(runLabels),
		ExposedPorts: nat.PortSet{containerPort: struct{}{}},
	}

	hostCfg := &container.HostConfig{
		Runtime:     d.opts.Runtime, // 빈 값이면 데몬 기본값
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		PortBindings: nat.PortMap{
			containerPort: []nat.PortBinding{binding},
		},
	}
	if d.opts.MemoryLimit != "" {
		// CLI는 "256m" 같은 문자열을 받았지만 API는 바이트 수를 받는다.
		limit, err := units.RAMInBytes(d.opts.MemoryLimit)
		if err != nil {
			return "", 0, fmt.Errorf("메모리 상한 값을 해석하지 못했습니다 (%q): %w", d.opts.MemoryLimit, err)
		}
		hostCfg.Memory = limit
	}
	if d.opts.PidsLimit > 0 {
		limit := int64(d.opts.PidsLimit)
		hostCfg.PidsLimit = &limit
	}

	// 네트워크를 지정하면 그 네트워크에 붙인다. 비면 기본 bridge(web).
	netCfg := &network.NetworkingConfig{}
	if spec.Network != "" {
		netCfg.EndpointsConfig = map[string]*network.EndpointSettings{spec.Network: {}}
	}

	created, err := d.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return "", 0, fmt.Errorf("컨테이너 생성 실패: %w", err)
	}
	instanceID = created.ID

	if err := d.cli.ContainerStart(ctx, instanceID, container.StartOptions{}); err != nil {
		// 만들었지만 못 띄웠다. 메모리 누수를 차단하기 위해 삭제
		_ = d.Remove(ctx, name)
		return "", 0, fmt.Errorf("컨테이너 실행 실패: %w", err)
	}

	// 데몬이 고른 host 포트는 띄운 뒤에야 정해진다.
	hostPort, err = d.publishedPort(ctx, instanceID, containerPort)
	if err != nil {
		_ = d.Remove(ctx, name)
		return "", 0, err
	}
	return instanceID, hostPort, nil
}

// 컨테이너가 떠 있는지와 열린 host 포트.
func (d *Docker) Running(ctx context.Context, nameOrID string) (running bool, hostPort int, err error) {
	info, err := d.cli.ContainerInspect(ctx, nameOrID)
	if err != nil {
		// 컨테이너가 존재하지 않음. 조회 실패와 구분하지 않는다
		return false, 0, nil
	}
	if info.State == nil || !info.State.Running {
		return false, 0, nil
	}

	port, err := firstExposedPort(info.Config.ExposedPorts)
	if err != nil {
		return true, 0, nil // 떠 있지만 포트를 모른다
	}
	hostPort, err = portFromBindings(info.NetworkSettings.Ports, port)
	if err != nil {
		return true, 0, nil
	}
	return true, hostPort, nil
}

// 모니터링에 쓰는 컨테이너 정보 (Feature-01).
type ContainerInfo struct {
	ID       string
	Name     string
	Labels   map[string]string
	Running  bool
	Created  time.Time
	HostPort int // 열린 host 포트. 없으면 0
}

// nimda.ctf 라벨이 붙은 컨테이너를 전부 가져온다. 멈춘 것도 포함한다.
// orphan(registry에 기록이 없는 컨테이너)을 찾으려면 멈춘 것까지 봐야 하기 때문이다.
func (d *Docker) List(ctx context.Context) ([]ContainerInfo, error) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelMarker)),
	})
	if err != nil {
		return nil, fmt.Errorf("컨테이너 목록 조회 실패: %w", err)
	}
	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		info := ContainerInfo{
			ID:      c.ID,
			Labels:  c.Labels,
			Running: c.State == "running",
			Created: time.Unix(c.Created, 0).UTC(),
		}
		if len(c.Names) > 0 {
			info.Name = strings.TrimPrefix(c.Names[0], "/")
		}
		for _, p := range c.Ports {
			if p.PublicPort != 0 {
				info.HostPort = int(p.PublicPort)
				break
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// 지금 떠 있는 컨테이너가 쓰는 pwn 포트를 모은다(이슈 #29의 InitPortPool 입력).
// nimda.ctf.port 라벨값을 읽는다. 라벨이 없거나(web) 숫자가 아니면 건너뛴다.
func (d *Docker) UsedPorts(ctx context.Context) ([]int, error) {
	list, err := d.List(ctx)
	if err != nil {
		return nil, err
	}
	ports := make([]int, 0, len(list))
	for _, c := range list {
		raw, ok := c.Labels[labelPort]
		if !ok {
			continue
		}
		p, err := strconv.Atoi(raw)
		if err != nil {
			continue
		}
		ports = append(ports, p)
	}
	return ports, nil
}

// 컨테이너를 삭제 없는 컨테이너는 성공으로 판단
func (d *Docker) Remove(ctx context.Context, nameOrID string) error {
	err := d.cli.ContainerRemove(ctx, nameOrID, container.RemoveOptions{Force: true})
	if err != nil && !client.IsErrNotFound(err) {
		return fmt.Errorf("컨테이너 삭제 실패: %w", err)
	}
	return nil
}

func (d *Docker) RemoveImage(ctx context.Context, imageTag string) {
	_, _ = d.cli.ImageRemove(ctx, imageTag, image.RemoveOptions{Force: true})
}

// 이미지가 EXPOSE한 포트를 읽는다. 예: "5000/tcp".
func (d *Docker) exposedPort(ctx context.Context, imageTag string) (nat.Port, error) {
	info, err := d.cli.ImageInspect(ctx, imageTag)
	if err != nil {
		return "", fmt.Errorf("이미지 EXPOSE 포트 조회 실패: %w", err)
	}
	if info.Config == nil {
		return "", fmt.Errorf("이미지 %s 에 EXPOSE된 포트가 없습니다 (Dockerfile에 EXPOSE 필요)", imageTag)
	}
	port, err := firstExposedPort(info.Config.ExposedPorts)
	if err != nil {
		return "", fmt.Errorf("이미지 %s 에 EXPOSE된 포트가 없습니다 (Dockerfile에 EXPOSE 필요)", imageTag)
	}
	return port, nil
}

// 데몬이 이 컨테이너 포트를 어느 host 포트에 열었는지 읽는다.
func (d *Docker) publishedPort(ctx context.Context, containerID string, port nat.Port) (int, error) {
	info, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, fmt.Errorf("host 포트 조회 실패: %w", err)
	}
	return portFromBindings(info.NetworkSettings.Ports, port)
}

func portFromBindings(ports nat.PortMap, port nat.Port) (int, error) {
	bindings := ports[port]
	if len(bindings) == 0 {
		return 0, fmt.Errorf("컨테이너 포트 %s 가 host에 열려 있지 않습니다", port)
	}
	var hostPort int
	if _, err := fmt.Sscanf(bindings[0].HostPort, "%d", &hostPort); err != nil {
		return 0, fmt.Errorf("host 포트 숫자 파싱 실패: %q", bindings[0].HostPort)
	}
	return hostPort, nil
}

// EXPOSE된 포트 중 하나를 고른다.
//
// 정렬해서 가장 앞을 쓴다. Go의 맵 순회는 순서가 매번 달라서, 정렬하지 않으면 EXPOSE가
// 둘 이상인 이미지가 실행할 때마다 다른 포트를 열게 된다.
// 타입 매개변수를 쓰는 이유: 같은 "EXPOSE된 포트 집합"인데 어디서 읽느냐에 따라 맵의 키
// 타입이 다르다. 컨테이너를 inspect하면 nat.PortSet(map[nat.Port]struct{})이고,
// 이미지를 inspect하면 OCI 이미지 설정이라 map[string]struct{}다.
func firstExposedPort[K ~string](ports map[K]struct{}) (nat.Port, error) {
	if len(ports) == 0 {
		return "", fmt.Errorf("EXPOSE된 포트 없음")
	}
	keys := make([]string, 0, len(ports))
	for p := range ports {
		keys = append(keys, string(p))
	}
	sort.Strings(keys)
	return nat.Port(keys[0]), nil
}

func envList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	list := make([]string, 0, len(env))
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	sort.Strings(list) // 재현 가능하게
	return list
}

func containerLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out[labelMarker] = "1"
	return out
}

// 빌드 출력 스트림을 끝까지 읽으면서 실패를 찾는다.
func drainBuildOutput(r io.Reader) error {
	dec := json.NewDecoder(r)
	for {
		var line struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("빌드 출력 해석 실패: %w", err)
		}
		if line.Error != "" || line.ErrorDetail.Message != "" {
			msg := line.ErrorDetail.Message
			if msg == "" {
				msg = line.Error
			}
			return fmt.Errorf("도커 빌드 실패: %s", msg)
		}
	}
}

// 폴더를 tar 스트림으로 만든다. 데몬이 빌드 컨텍스트를 이 형식으로 받는다.
func tarDir(dir string) (io.ReadCloser, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && !info.IsDir() {
				return nil
			}

			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}

			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := tw.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()
	return pr, nil
}

func findDockerfileDir(root string) (string, error) {
	var found string
	bestDepth := -1
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || info.Name() != "Dockerfile" {
			return nil
		}
		depth := strings.Count(strings.TrimPrefix(path, root), string(os.PathSeparator))
		if bestDepth == -1 || depth < bestDepth {
			bestDepth = depth
			found = filepath.Dir(path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("빌드 컨텍스트에 Dockerfile이 없습니다")
	}
	return found, nil
}
