package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	defaultStateDir       = "/var/lib/peer-pod-node"
	defaultContainerdSock = "/run/containerd/containerd.sock"
)

type process struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

type processExitError struct {
	name string
	err  error
}

func (e *processExitError) Error() string {
	return fmt.Sprintf("%s exited: %v", e.name, e.err)
}

func (e *processExitError) Unwrap() error { return e.err }

func main() {
	if err := run(); err != nil {
		log.Printf("peer-pod-node: %v", err)
		os.Exit(1)
	}
}

func run() error {
	bootstrap := env("PEER_POD_NODE_BOOTSTRAP_KUBECONFIG", "/etc/peer-pod-node/bootstrap-kubeconfig")
	if _, err := os.Stat(bootstrap); err != nil {
		return fmt.Errorf("bootstrap kubeconfig: %w", err)
	}
	nodeName := env("PEER_POD_NODE_NAME", hostname())
	stateDir := env("PEER_POD_NODE_STATE_DIR", defaultStateDir)
	directories := []string{
		stateDir, "/run/containerd", "/run/peerpod", "/run/netns", "/var/lib/containerd", "/var/lib/kubelet",
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	if err := ensureUnixSyslogSocket(); err != nil {
		return err
	}
	kubeletConfig, err := prepareKubeletConfig(stateDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	processes := []*process{
		command("containerd", "/usr/local/bin/containerd", "--config", "/etc/containerd/config.toml"),
	}
	if err := start(processes[0]); err != nil {
		return err
	}
	defer func() {
		terminate(processes, 30*time.Second)
		if err := cleanupNetworkNamespaces("/run/netns", syscall.Unmount); err != nil {
			log.Printf("clean up network namespaces: %v", err)
		}
	}()
	if err := waitForPath(ctx, defaultContainerdSock, 30*time.Second, processes[0]); err != nil {
		return err
	}

	kubelet := command("kubelet", "/usr/local/bin/kubelet",
		"--bootstrap-kubeconfig="+bootstrap,
		"--kubeconfig="+filepath.Join(stateDir, "kubeconfig"),
		"--cert-dir="+filepath.Join(stateDir, "pki"),
		"--config="+kubeletConfig,
		"--container-runtime-endpoint=unix://"+defaultContainerdSock,
		"--hostname-override="+nodeName,
		"--node-labels=kata.peerpods.io/node=true,"+env("PEER_POD_NODE_LABELS", "node.kubernetes.io/worker="),
		"--register-with-taints="+env("PEER_POD_NODE_TAINTS", "kata.peerpods.io/node=true:NoSchedule"),
		"--root-dir="+filepath.Join(stateDir, "kubelet"),
	)
	processes = append(processes, kubelet)
	if err := start(kubelet); err != nil {
		return err
	}

	pluginPath := "/cloud-providers/kubernetes.so"
	pluginHash, err := fileSHA256(pluginPath)
	if err != nil {
		return fmt.Errorf("hash CAA provider plugin: %w", err)
	}
	caa, err := startCAA(ctx, nodeName, pluginPath, pluginHash, kubelet)
	if err != nil {
		return err
	}
	processes = append(processes, caa)

	health := &http.Server{
		Addr:    env("PEER_POD_NODE_HEALTH_ADDRESS", ":8081"),
		Handler: healthHandler(defaultContainerdSock),
	}
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stop()
		}
	}()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = health.Shutdown(shutdown)
	}()

	exited := make(chan error, len(processes))
	for _, managed := range processes {
		go func(p *process) {
			<-p.done
			err := p.err
			if err == nil {
				err = errors.New("process exited without an error")
			}
			exited <- &processExitError{name: p.name, err: err}
		}(managed)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-exited:
		return err
	}
}

func ensureUnixSyslogSocket() error {
	const (
		link   = "/dev/log"
		target = "/run/systemd/journal/dev-log"
	)
	if info, err := os.Stat(target); err != nil {
		return fmt.Errorf("syslog socket: %w", err)
	} else if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("syslog socket: %s is not a Unix socket", target)
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale syslog socket: %w", err)
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("create syslog socket link: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func startCAA(ctx context.Context, nodeName, pluginPath, pluginHash string, kubelet *process) (*process, error) {
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		caa := command("cloud-api-adaptor", "/usr/local/bin/cloud-api-adaptor", "kubernetes")
		caa.cmd.Env = append(os.Environ(),
			"ENABLE_CLOUD_PROVIDER_EXTERNAL_PLUGIN=true",
			"CLOUD_PROVIDER_EXTERNAL_PLUGIN_PATH="+pluginPath,
			"CLOUD_PROVIDER_EXTERNAL_PLUGIN_HASH="+pluginHash,
			"CRI_RUNTIME_ENDPOINT="+defaultContainerdSock,
			"NODE_NAME="+nodeName,
		)
		if err := start(caa); err != nil {
			return nil, err
		}
		err := waitForPath(ctx, "/run/peerpod/hypervisor.sock", 15*time.Second, caa, kubelet)
		if err == nil {
			return caa, nil
		}
		var exited *processExitError
		if errors.As(err, &exited) && exited.name == kubelet.name {
			return nil, err
		}
		if exited == nil || exited.name != caa.name {
			_ = caa.cmd.Process.Signal(syscall.SIGTERM)
			<-caa.done
		}
		log.Printf("cloud-api-adaptor is waiting for PeerPodNode registration: %v", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("timed out starting cloud-api-adaptor after PeerPodNode registration")
		case <-time.After(time.Second):
		}
	}
}

func prepareKubeletConfig(stateDir string) (string, error) {
	if configured := strings.TrimSpace(os.Getenv("PEER_POD_NODE_KUBELET_CONFIG")); configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("kubelet config: %w", err)
		}
		return configured, nil
	}
	dns := strings.TrimSpace(os.Getenv("PEER_POD_NODE_CLUSTER_DNS"))
	if net.ParseIP(dns) == nil {
		return "", errors.New("PEER_POD_NODE_CLUSTER_DNS must contain a valid cluster DNS IP address")
	}
	domain := env("PEER_POD_NODE_CLUSTER_DOMAIN", "cluster.local")
	clientCA := env("PEER_POD_NODE_CLIENT_CA_FILE", "/etc/peer-pod-node/cluster-ca.crt")
	if _, err := os.Stat(clientCA); err != nil {
		return "", fmt.Errorf("kubelet client CA: %w", err)
	}
	path := filepath.Join(stateDir, "kubelet.yaml")
	contents := fmt.Sprintf(`apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
authentication:
  anonymous:
    enabled: false
  webhook:
    enabled: true
  x509:
    clientCAFile: %s
authorization:
  mode: Webhook
cgroupDriver: cgroupfs
clusterDomain: %s
clusterDNS:
  - %s
failSwapOn: false
registerNode: true
rotateCertificates: true
serverTLSBootstrap: true
staticPodPath: /etc/peer-pod-node/manifests
`, clientCA, domain, dns)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func command(name, executable string, args ...string) *process {
	cmd := exec.Command(executable, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return &process{name: name, cmd: cmd}
}

func start(p *process) error {
	log.Printf("starting %s", p.name)
	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.name, err)
	}
	p.done = make(chan struct{})
	go func() {
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	return nil
}

func terminate(processes []*process, timeout time.Duration) {
	for _, managed := range slices.Backward(processes) {
		if managed.cmd.Process != nil {
			_ = managed.cmd.Process.Signal(syscall.SIGTERM)
		}
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, managed := range slices.Backward(processes) {
		if managed.done == nil {
			continue
		}
		select {
		case <-managed.done:
		case <-deadline.C:
			for _, managed := range processes {
				if managed.cmd.Process != nil && managed.cmd.ProcessState == nil {
					_ = managed.cmd.Process.Kill()
				}
			}
			return
		}
	}
}

func cleanupNetworkNamespaces(directory string, unmount func(string, int) error) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var cleanupErrors []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		err := unmount(path, syscall.MNT_DETACH)
		if err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOENT) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("unmount %s: %w", path, err))
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func waitForPath(ctx context.Context, path string, timeout time.Duration, processes ...*process) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		for _, managed := range processes {
			select {
			case <-managed.done:
				return &processExitError{name: managed.name, err: managed.err}
			default:
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %s", path)
		case <-ticker.C:
		}
	}
}

func healthHandler(containerdSocket string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		for _, path := range []string{containerdSocket, "/run/peerpod/hypervisor.sock"} {
			if _, err := os.Stat(path); err != nil {
				http.Error(w, path+" is unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func hostname() string {
	value, err := os.Hostname()
	if err != nil || value == "" {
		return "peer-pod-node"
	}
	return value
}
