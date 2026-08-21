package main

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderNetworkConfig(t *testing.T) {
	result := renderNetworkConfig(networkState{
		Address: "10.42.0.8/24",
		Gateway: "10.42.0.1",
		DNS:     []string{"10.96.0.10"},
		MAC:     "02:00:00:00:00:08",
	}, 1400)
	for _, want := range []string{"10.42.0.8/24", "10.42.0.1", "10.96.0.10", "02:00:00:00:00:08", "set-name: eth0", "mtu: 1400"} {
		if !strings.Contains(result, want) {
			t.Errorf("network config does not contain %q:\n%s", want, result)
		}
	}
}

func TestRandomMACIsLocallyAdministeredUnicast(t *testing.T) {
	raw, err := randomMAC()
	if err != nil {
		t.Fatal(err)
	}
	mac, err := net.ParseMAC(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mac[0]&2 == 0 || mac[0]&1 != 0 {
		t.Fatalf("MAC %s is not locally administered unicast", raw)
	}
}

func TestCloudHypervisorArgsDefaultBootsFirmwareFromQcow2Overlay(t *testing.T) {
	args := cloudHypervisorArgs(options{cpus: 1, memoryMiB: 512, firmware: "/usr/share/cloud-hypervisor/CLOUDHV.fd"}, "/sock", "/root.qcow2", "/cidata.img", "02:00:00:00:00:08")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--firmware /usr/share/cloud-hypervisor/CLOUDHV.fd", "path=/root.qcow2,image_type=qcow2,backing_files=on", "path=/cidata.img,readonly=on,image_type=raw"} {
		if !strings.Contains(joined, want) {
			t.Errorf("default args do not contain %q:", want)
		}
	}
	if strings.Contains(joined, "--kernel") {
		t.Errorf("default args unexpectedly use direct-kernel boot")
	}
}

func TestReadyCheckRequiresCloudHypervisorAPI(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cloud-hypervisor.sock")
	agent := startAgentListener(t)
	if err := readyCheck(socket, agent); err == nil {
		t.Fatal("readyCheck succeeded without a Cloud Hypervisor API socket")
	}
}

func TestReadyCheckRequiresGuestAgent(t *testing.T) {
	socket := startCloudHypervisorAPI(t)
	if err := readyCheck(socket, "127.0.0.1:0"); err == nil {
		t.Fatal("readyCheck succeeded without a reachable guest agent")
	}
}

func TestReadyCheckSucceedsWhenVMAndAgentAreReady(t *testing.T) {
	socket := startCloudHypervisorAPI(t)
	agent := startAgentListener(t)
	if err := readyCheck(socket, agent); err != nil {
		t.Fatalf("readyCheck failed for a ready PodVM: %v", err)
	}
}

// startCloudHypervisorAPI serves vm.info on a unix socket so readyCheck can
// exercise the real HTTP-over-unix client path.
func startCloudHypervisorAPI(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "cloud-hypervisor.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/vm.info" {
			http.NotFound(response, request)
			return
		}
		response.WriteHeader(http.StatusOK)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

// startAgentListener accepts connections like the guest agent-protocol-forwarder.
func startAgentListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	return listener.Addr().String()
}
