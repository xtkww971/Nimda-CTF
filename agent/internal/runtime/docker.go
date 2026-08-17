// 컨테이너 대신 docker CLI와 상호작용하는 프로세스
package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Options tune how instances are run.
type Options struct {
	// Runtime is the container runtime name passed to `docker --runtime`.
	// Empty = the daemon default. In production this is "runsc" (gVisor,
	// ADR-0008); on a dev host without gVisor leave it empty.
	Runtime string

	// MemoryLimit / PidsLimit bound each instance (fork-bomb / memory-hog
	// defense). Example: "256m", 256.
	MemoryLimit string
	PidsLimit   int
}

type Docker struct {
	opts Options
}

func NewDocker(opts Options) *Docker {
	return &Docker{opts: opts}
}

// BuildImage extracts the build-context tar, finds the Dockerfile inside it,
// and builds an image tagged imageTag. The challenge zip wraps its files in a
// folder, so the Dockerfile may sit one level down - we locate it and build
// from its directory.
func (d *Docker) BuildImage(ctx context.Context, buildContext []byte, imageTag string) error {
	tmp, err := os.MkdirTemp("", "ctf-build-*")
	if err != nil {
		return fmt.Errorf("빌드 임시 폴더 생성 실패: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := untar(buildContext, tmp); err != nil {
		return fmt.Errorf("빌드 컨텍스트 해제 실패: %w", err)
	}

	contextDir, err := findDockerfileDir(tmp)
	if err != nil {
		return err
	}

	out, err := d.run(ctx, "build", "-t", imageTag, contextDir)
	if err != nil {
		return fmt.Errorf("docker build 실패: %w\n%s", err, out)
	}
	return nil
}

// labelMarker tags every instance container so List can find them.
const labelMarker = "nimda.ctf"

// Instance is one running challenge container as seen by List.
type Instance struct {
	ID            string
	ChallengeCode string
	UserID        int64
	Port          int
}

// Run starts a container from imageTag, publishes its exposed port to a random
// host port on 127.0.0.1, and returns the container id and that host port.
// A container already using this name is removed first (fresh start). labels
// are attached so List can later report this instance.
func (d *Docker) Run(ctx context.Context, imageTag, name string, env, labels map[string]string) (instanceID string, hostPort int, err error) {
	containerPort, err := d.exposedPort(ctx, imageTag)
	if err != nil {
		return "", 0, err
	}

	// 같은 이름의 컨테이너 존재시 삭제 후 다시 빌드
	_, _ = d.run(ctx, "rm", "-f", name)

	args := []string{"run", "-d", "--name", name}
	if d.opts.Runtime != "" {
		args = append(args, "--runtime", d.opts.Runtime)
	}
	// Hardening (ADR-0007 D4). Egress restriction is a network-policy concern
	// handled at deploy time, not here.
	args = append(args,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
	)
	if d.opts.MemoryLimit != "" {
		args = append(args, "--memory", d.opts.MemoryLimit)
	}
	if d.opts.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", d.opts.PidsLimit))
	}
	args = append(args, "--label", labelMarker+"=1")
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}
	// Publish the container's exposed port to a random host port on loopback.
	args = append(args, "-p", "127.0.0.1:0:"+containerPort, imageTag)

	out, err := d.run(ctx, args...)
	if err != nil {
		return "", 0, fmt.Errorf("docker run 실패: %w\n%s", err, out)
	}
	instanceID = strings.TrimSpace(out)

	// 생성했지만 접근하지 못할 때 삭제
	hostPort, err = d.publishedPort(ctx, name, containerPort)
	if err != nil {
		// Container is up but we couldn't read its port - tear it down so we
		// don't leak it, and report failure.
		_, _ = d.run(ctx, "rm", "-f", name)
		return "", 0, err
	}
	return instanceID, hostPort, nil
}

// 작동 컨테이너 확인
func (d *Docker) Running(ctx context.Context, name string) (running bool, hostPort int, err error) {
	out, err := d.run(ctx, "inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		// No such container.
		return false, 0, nil
	}
	if strings.TrimSpace(out) != "true" {
		return false, 0, nil
	}
	containerPort, err := d.exposedPortOfContainer(ctx, name)
	if err != nil {
		return true, 0, nil
	}
	port, err := d.publishedPort(ctx, name, containerPort)
	if err != nil {
		return true, 0, nil
	}
	return true, port, nil
}

// 작동 중인 컨테이너 확인
func (d *Docker) List(ctx context.Context) ([]Instance, error) {
	const format = "{{.ID}}|{{.Label \"nimda.ctf.code\"}}|{{.Label \"nimda.ctf.user\"}}|{{.Ports}}"
	out, err := d.run(ctx, "ps", "--filter", "label="+labelMarker, "--format", format)
	if err != nil {
		return nil, fmt.Errorf("docker ps 실패: %w\n%s", err, out)
	}

	var instances []Instance
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 4 {
			continue
		}
		inst := Instance{ID: parts[0], ChallengeCode: parts[1], Port: parsePortsColumn(parts[3])}
		if parts[2] != "" {
			if uid, perr := strconv.ParseInt(parts[2], 10, 64); perr == nil {
				inst.UserID = uid
			}
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

// parsePortsColumn pulls the host port out of docker ps's Ports column, e.g.
// "127.0.0.1:32768->8080/tcp" -> 32768. Returns 0 if none is published.
func parsePortsColumn(s string) int {
	first := strings.TrimSpace(strings.Split(s, ",")[0]) // first mapping
	arrow := strings.Index(first, "->")
	if arrow < 0 {
		return 0
	}
	hostPart := first[:arrow] // "127.0.0.1:32768"
	idx := strings.LastIndex(hostPart, ":")
	if idx < 0 {
		return 0
	}
	var port int
	if _, err := fmt.Sscanf(hostPart[idx+1:], "%d", &port); err != nil {
		return 0
	}
	return port
}

// 컨테이너 삭제
func (d *Docker) Remove(ctx context.Context, name string) error {
	out, err := d.run(ctx, "rm", "-f", name)
	if err != nil && !strings.Contains(out, "No such container") {
		return fmt.Errorf("docker rm 실패: %w\n%s", err, out)
	}
	return nil
}

// 이미지 삭제
func (d *Docker) RemoveImage(ctx context.Context, imageTag string) {
	_, _ = d.run(ctx, "rmi", "-f", imageTag)
}

// exposedPort reads the first EXPOSEd port of an image, e.g. "5000/tcp".
func (d *Docker) exposedPort(ctx context.Context, imageTag string) (string, error) {
	out, err := d.run(ctx, "inspect", "--format",
		"{{range $p, $v := .Config.ExposedPorts}}{{$p}} {{end}}", imageTag)
	if err != nil {
		return "", fmt.Errorf("이미지 EXPOSE 포트 조회 실패: %w\n%s", err, out)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("이미지 %s 에 EXPOSE된 포트가 없습니다 (Dockerfile에 EXPOSE 필요)", imageTag)
	}
	return fields[0], nil
}

func (d *Docker) exposedPortOfContainer(ctx context.Context, name string) (string, error) {
	out, err := d.run(ctx, "inspect", "--format",
		"{{range $p, $v := .Config.ExposedPorts}}{{$p}} {{end}}", name)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", fmt.Errorf("no exposed port")
	}
	return fields[0], nil
}

// publishedPort reads the host port a container port was mapped to, from
// `docker port <name> <containerPort>` -> "127.0.0.1:49153".
func (d *Docker) publishedPort(ctx context.Context, name, containerPort string) (int, error) {
	out, err := d.run(ctx, "port", name, containerPort)
	if err != nil {
		return 0, fmt.Errorf("host 포트 조회 실패: %w\n%s", err, out)
	}
	line := strings.TrimSpace(out)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		return 0, fmt.Errorf("host 포트 형식을 해석 못함: %q", line)
	}
	var port int
	if _, err := fmt.Sscanf(line[idx+1:], "%d", &port); err != nil {
		return 0, fmt.Errorf("host 포트 숫자 파싱 실패: %q", line)
	}
	return port, nil
}

func (d *Docker) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// findDockerfileDir returns the shallowest directory under root containing a
// file named "Dockerfile".
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

// untar extracts a tar into dest, guarding against path traversal (tar-slip).
func untar(tarData []byte, dest string) error {
	tr := tar.NewReader(bytes.NewReader(tarData))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, hdr.Name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("잘못된 경로(tar-slip): %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}
