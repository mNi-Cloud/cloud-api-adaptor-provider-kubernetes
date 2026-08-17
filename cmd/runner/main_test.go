package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestStartupGraceElapsed(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "cloud-hypervisor.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := startupGraceElapsed(socket, time.Minute); err == nil {
		t.Fatal("startup grace unexpectedly elapsed for a new socket")
	}

	started := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(socket, started, started); err != nil {
		t.Fatal(err)
	}
	if err := startupGraceElapsed(socket, time.Minute); err != nil {
		t.Fatalf("startup grace did not elapse: %v", err)
	}
	if err := startupGraceElapsed(socket, 0); err != nil {
		t.Fatalf("zero startup grace must be disabled: %v", err)
	}
}
