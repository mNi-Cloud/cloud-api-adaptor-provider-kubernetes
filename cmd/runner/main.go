package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultStateDir    = "/run/pod-sandbox"
	primaryLink        = "eth0"
	bridgeName         = "podvm-br0"
	tapName            = "podvm-tap0"
	managementCIDR     = "192.0.2.0/30"
	hostAddress        = "192.0.2.1/30"
	guestAddress       = "192.0.2.2/30"
	guestIP            = "192.0.2.2"
	forwarderPort      = "15150"
	proxyPort          = "3128"
	vxlanPort          = "4789"
	commandAdd         = "add"
	commandDevice      = "dev"
	commandSet         = "set"
	commandIPTables    = "iptables"
	commandLink        = "link"
	chainForward       = "FORWARD"
	targetAccept       = "ACCEPT"
	directKernelParams = "reboot=k panic=1 systemd.unit=podvm.target quiet " +
		"root=/dev/vda1 rootflags=data=ordered,errors=remount-ro rw rootfstype=ext4 " +
		"no_timer_check noreplace-smp console=ttyS0 systemd.log_target=console " +
		"cgroup_no_v1=all systemd.unified_cgroup_hierarchy=1"
)

type options struct {
	cpus             int
	memoryMiB        int
	networkMTU       int
	imageDir         string
	configDir        string
	stateDir         string
	runtimeAssetsDir string
	firmware         string
	kernelPath       string
	initramfsPath    string
	rootfsPath       string
	kernelParams     string
}

type readyOptions struct {
	stateDir string
}

type networkState struct {
	Address string
	Gateway string
	DNS     []string
	MAC     string
}

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: runner <run|ready|stop>")
	}
	switch os.Args[1] {
	case "run":
		if err := run(parseRunOptions(os.Args[2:])); err != nil {
			fatalf("run PodVM: %v", err)
		}
	case "ready":
		opts := parseReadyOptions(os.Args[2:])
		socket := filepath.Join(opts.stateDir, "cloud-hypervisor.sock")
		if err := readyCheck(socket, net.JoinHostPort(guestIP, forwarderPort)); err != nil {
			os.Exit(1)
		}
	case "stop":
		stateDir := parseStateDir("stop", os.Args[2:])
		if err := shutdown(filepath.Join(stateDir, "cloud-hypervisor.sock")); err != nil {
			fatalf("stop PodVM: %v", err)
		}
	default:
		fatalf("unknown command %q", os.Args[1])
	}
}

func parseRunOptions(args []string) options {
	var opts options
	flags := flag.NewFlagSet("run", flag.ExitOnError)
	flags.IntVar(&opts.cpus, "cpus", 1, "number of PodVM CPUs")
	flags.IntVar(&opts.memoryMiB, "memory-mib", 512, "PodVM memory in MiB")
	flags.IntVar(&opts.networkMTU, "network-mtu", 1400, "PodVM network MTU")
	flags.StringVar(&opts.imageDir, "image-dir", "", "directory containing PodVM image artifacts")
	flags.StringVar(&opts.configDir, "config-dir", "", "directory containing PodVM configuration artifacts")
	flags.StringVar(&opts.stateDir, "state-dir", defaultStateDir, "writable PodVM state directory")
	flags.StringVar(&opts.runtimeAssetsDir, "runtime-assets-dir", "/opt/podvm-runtime", "PodVM runtime assets directory")
	flags.StringVar(&opts.firmware, "firmware", "/usr/share/cloud-hypervisor/CLOUDHV.fd", "Cloud Hypervisor UEFI firmware")
	flags.StringVar(
		&opts.kernelPath, "kernel", "",
		"kernel image for direct-kernel boot; when set, the PodVM boots without firmware",
	)
	flags.StringVar(&opts.initramfsPath, "initramfs", "", "initramfs image for direct-kernel boot")
	flags.StringVar(
		&opts.rootfsPath, "rootfs", "",
		"raw rootfs disk image for direct-kernel boot; when set, no qcow2 overlay is created",
	)
	flags.StringVar(
		&opts.kernelParams, "kernel-params", "",
		"kernel command line parameters (space-separated) for direct-kernel boot",
	)
	_ = flags.Parse(args)
	return opts
}

func parseStateDir(name string, args []string) string {
	flags := flag.NewFlagSet(name, flag.ExitOnError)
	stateDir := flags.String("state-dir", defaultStateDir, "PodVM state directory")
	_ = flags.Parse(args)
	return *stateDir
}

func parseReadyOptions(args []string) readyOptions {
	var opts readyOptions
	flags := flag.NewFlagSet("ready", flag.ExitOnError)
	flags.StringVar(&opts.stateDir, "state-dir", defaultStateDir, "PodVM state directory")
	_ = flags.Parse(args)
	return opts
}

// readyCheck reports whether the PodVM is ready to serve CAA requests. It
// succeeds only when the Cloud Hypervisor API socket answers vm.info and the
// guest agent-protocol-forwarder accepts connections. The forwarder is started
// by cloud-init after guest initialization, so its TCP readiness is the
// guest-side signal that initialization has completed.
func readyCheck(socket, agentTarget string) error {
	if err := vmInfo(socket); err != nil {
		return err
	}
	return agentReady(agentTarget)
}

func run(opts options) error {
	if opts.cpus < 1 || opts.memoryMiB < 128 {
		return fmt.Errorf("invalid capacity: cpus=%d memoryMiB=%d", opts.cpus, opts.memoryMiB)
	}
	if opts.imageDir == "" || opts.configDir == "" {
		return errors.New("--image-dir and --config-dir are required")
	}
	if err := os.MkdirAll(opts.stateDir, 0o700); err != nil {
		return err
	}
	guestMAC, err := randomMAC()
	if err != nil {
		return err
	}
	network := networkState{
		Address: guestAddress,
		Gateway: strings.SplitN(hostAddress, "/", 2)[0],
		DNS:     nameservers(),
		MAC:     guestMAC,
	}
	if err := prepareDisks(opts, network, guestMAC); err != nil {
		return err
	}
	if err := prepareNetwork(primaryLink, bridgeName, tapName, opts.networkMTU); err != nil {
		return err
	}
	listener, err := startAgentProxy(forwarderPort, net.JoinHostPort(guestIP, forwarderPort))
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	proxyServer, err := startHTTPProxy(net.JoinHostPort(network.Gateway, proxyPort))
	if err != nil {
		return err
	}
	defer func() { _ = proxyServer.Close() }()

	socket := filepath.Join(opts.stateDir, "cloud-hypervisor.sock")
	_ = os.Remove(socket)
	cloudHypervisor := filepath.Join(opts.runtimeAssetsDir, "bin", "cloud-hypervisor")
	rootDisk := filepath.Join(opts.stateDir, "root.qcow2")
	seedDisk := filepath.Join(opts.stateDir, "cidata.img")
	cmd := exec.Command(cloudHypervisor, cloudHypervisorArgs(opts, socket, rootDisk, seedDisk, guestMAC)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case sig := <-signals:
		fmt.Fprintf(os.Stderr, "received %s, shutting down PodVM\n", sig)
		_ = shutdown(socket)
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return cmd.Process.Kill()
		}
	case err := <-done:
		return err
	}
}

// cloudHypervisorArgs renders the Cloud Hypervisor command line for a run.
// Without --kernel the PodVM boots through UEFI firmware from a qcow2 overlay
// backed by the image directory. When --kernel is set, the PodVM boots the
// kernel directly with an optional initramfs and an optional raw rootfs disk,
// which skips firmware (the current default path) for a faster cold start.
func cloudHypervisorArgs(opts options, socket, rootDisk, seedDisk, guestMAC string) []string {
	args := []string{
		"--api-socket", socket,
		"--cpus", fmt.Sprintf("boot=%d", opts.cpus),
		"--memory", fmt.Sprintf("size=%dM", opts.memoryMiB),
	}
	if opts.kernelPath == "" {
		args = append(args,
			"--firmware", opts.firmware,
			"--disk",
			fmt.Sprintf("path=%s,image_type=qcow2,backing_files=on", rootDisk),
			fmt.Sprintf("path=%s,readonly=on,image_type=raw", seedDisk),
		)
	} else {
		args = append(args, "--kernel", opts.kernelPath)
		if opts.initramfsPath != "" {
			args = append(args, "--initramfs", opts.initramfsPath)
		}
		params := strings.TrimSpace(opts.kernelParams)
		if params == "" {
			params = directKernelParams
		}
		args = append(args, "--cmdline", params)
		args = append(args, "--disk")
		if opts.rootfsPath != "" {
			args = append(args, fmt.Sprintf("path=%s,image_type=raw", opts.rootfsPath))
		}
		args = append(args, fmt.Sprintf("path=%s,readonly=on,image_type=raw", seedDisk))
	}
	return append(args,
		"--net", fmt.Sprintf("tap=%s,mac=%s", tapName, guestMAC),
		"--serial", "tty",
		"--console", "off",
	)
}

func prepareDisks(opts options, network networkState, guestMAC string) error {
	rootDisk := filepath.Join(opts.stateDir, "root.qcow2")
	image := filepath.Join(opts.imageDir, "disk.qcow2")
	if opts.kernelPath == "" {
		if err := command("qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", image, rootDisk); err != nil {
			return fmt.Errorf("create root overlay: %w", err)
		}
	}
	seedDir := filepath.Join(opts.stateDir, "cidata")
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		return err
	}
	userData, err := os.ReadFile(filepath.Join(opts.configDir, "userdata"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(seedDir, "user-data"), userData, 0o600); err != nil {
		return err
	}
	// A reused instance ID causes cloud-init to keep the state embedded in the
	// immutable base image. The guest MAC is unique per Runner, so it also gives
	// every PodVM a stable identity for the lifetime of this Runner Pod.
	meta := fmt.Sprintf("instance-id: pod-sandbox-%s\nlocal-hostname: peerpod\n", strings.ReplaceAll(guestMAC, ":", ""))
	if err := os.WriteFile(filepath.Join(seedDir, "meta-data"), []byte(meta), 0o600); err != nil {
		return err
	}
	networkConfig := renderNetworkConfig(network, opts.networkMTU)
	if err := os.WriteFile(filepath.Join(seedDir, "network-config"), []byte(networkConfig), 0o600); err != nil {
		return err
	}
	seedDisk := filepath.Join(opts.stateDir, "cidata.img")
	if opts.kernelPath != "" {
		if err := command("mke2fs", "-q", "-t", "ext4", "-L", "cidata", "-d", seedDir, seedDisk, "4M"); err != nil {
			return fmt.Errorf("create direct-kernel config disk: %w", err)
		}
		return nil
	}
	if err := command(
		"genisoimage", "-quiet", "-output", seedDisk,
		"-volid", "cidata", "-joliet", "-rock", seedDir,
	); err != nil {
		return fmt.Errorf("create NoCloud seed: %w", err)
	}
	return nil
}

func nameservers() []string {
	content, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var result []string
	for line := range strings.SplitSeq(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" && net.ParseIP(fields[1]) != nil {
			result = append(result, fields[1])
		}
	}
	return result
}

func renderNetworkConfig(network networkState, mtu int) string {
	var dns string
	if len(network.DNS) > 0 {
		dns = "    nameservers:\n      addresses: [" + strings.Join(network.DNS, ", ") + "]\n"
	}
	const networkTemplate = `version: 2
ethernets:
  primary:
    match:
      macaddress: %q
    set-name: eth0
    addresses: [%s]
    routes:
      - to: 0.0.0.0/0
        via: %s
%s    mtu: %d
`
	return fmt.Sprintf(networkTemplate, network.MAC, network.Address, network.Gateway, dns, mtu)
}

func prepareNetwork(linkName, bridge, tap string, mtu int) error {
	commands := [][]string{
		{"ip", commandLink, commandAdd, bridge, "type", "bridge"},
		{"ip", "tuntap", commandAdd, commandDevice, tap, "mode", "tap"},
		{"ip", commandLink, commandSet, commandDevice, bridge, "mtu", strconv.Itoa(mtu)},
		{"ip", commandLink, commandSet, commandDevice, tap, "mtu", strconv.Itoa(mtu)},
		{"ip", commandLink, commandSet, commandDevice, tap, "master", bridge},
		{"ip", "address", commandAdd, hostAddress, commandDevice, bridge},
		{"ip", commandLink, commandSet, commandDevice, tap, "up"},
		{"ip", commandLink, commandSet, commandDevice, bridge, "up"},
		{"sysctl", "-w", "net.ipv4.ip_forward=1"},
		{commandIPTables, "-t", "nat", "-A", "POSTROUTING", "-s", managementCIDR,
			"-o", linkName, "-j", "MASQUERADE"},
		// CAA sends the workload VXLAN to the instance address returned by
		// this provider. That address belongs to the runner Pod, so forward the
		// tunnel into the PodVM management network where APF terminates it.
		{commandIPTables, "-t", "nat", "-A", "PREROUTING", "-i", linkName, "-p", "udp",
			"--dport", vxlanPort, "-j", "DNAT", "--to-destination", net.JoinHostPort(guestIP, vxlanPort)},
		{commandIPTables, "-A", chainForward, "-i", linkName, "-o", bridge, "-p", "udp",
			"-d", guestIP, "--dport", vxlanPort, "-j", targetAccept},
		{commandIPTables, "-A", chainForward, "-i", bridge, "-o", linkName, "-j", targetAccept},
		{commandIPTables, "-A", chainForward, "-i", linkName, "-o", bridge, "-m", "conntrack",
			"--ctstate", "ESTABLISHED,RELATED", "-j", targetAccept},
	}
	for _, args := range commands {
		if err := command(args[0], args[1:]...); err != nil {
			return fmt.Errorf("configure PodVM network (%s): %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func startAgentProxy(port, target string) (net.Listener, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort("", port))
	if err != nil {
		return nil, fmt.Errorf("listen for agent proxy: %w", err)
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go proxyConnection(connection, target)
		}
	}()
	return listener, nil
}

func agentReady(target string) error {
	connection, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		return err
	}
	return connection.Close()
}

func proxyConnection(connection net.Conn, target string) {
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		_ = connection.Close()
		return
	}
	bridgeConnections(connection, upstream)
}

func bridgeConnections(connection, upstream net.Conn) {
	defer func() { _ = connection.Close() }()
	defer func() { _ = upstream.Close() }()

	done := make(chan struct{}, 2)
	copyStream := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		if tcp, ok := destination.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyStream(upstream, connection)
	go copyStream(connection, upstream)
	<-done
}

func startHTTPProxy(address string) (*http.Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen for HTTP proxy: %w", err)
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(proxyHTTP),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}

func proxyHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodConnect {
		proxyConnect(response, request)
		return
	}

	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	outbound.Header.Del("Proxy-Connection")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	upstream, err := transport.RoundTrip(outbound)
	if err != nil {
		http.Error(response, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = upstream.Body.Close() }()
	for key, values := range upstream.Header {
		for _, value := range values {
			response.Header().Add(key, value)
		}
	}
	response.WriteHeader(upstream.StatusCode)
	_, _ = io.Copy(response, upstream.Body)
}

func proxyConnect(response http.ResponseWriter, request *http.Request) {
	upstream, err := net.DialTimeout("tcp", request.Host, 10*time.Second)
	if err != nil {
		http.Error(response, "upstream unavailable", http.StatusBadGateway)
		return
	}
	hijacker, ok := response.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(response, "CONNECT is unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	go bridgeConnections(client, upstream)
}

func randomMAC() (string, error) {
	mac := make([]byte, 6)
	if _, err := rand.Read(mac); err != nil {
		return "", err
	}
	mac[0] = (mac[0] | 2) & 0xfe
	return net.HardwareAddr(mac).String(), nil
}

// command is swappable so tests can stub qemu-img and genisoimage, which are
// not available in every development environment.
var command = runCommand

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func vmInfo(socket string) error {
	response, err := apiRequest(socket, http.MethodGet, "/api/v1/vm.info")
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return fmt.Errorf("cloud-hypervisor returned %s", response.Status)
	}
	return nil
}

func shutdown(socket string) error {
	response, err := apiRequest(socket, http.MethodPut, "/api/v1/vm.shutdown")
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("cloud-hypervisor returned %s", response.Status)
	}
	return nil
}

func apiRequest(socket, method, path string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, nil)
	if err != nil {
		return nil, err
	}
	return (&http.Client{Transport: transport}).Do(request)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
