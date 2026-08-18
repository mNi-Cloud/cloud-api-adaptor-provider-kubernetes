package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPrepareKubeletConfig(t *testing.T) {
	t.Setenv("PEER_POD_NODE_KUBELET_CONFIG", "")
	t.Setenv("PEER_POD_NODE_CLUSTER_DNS", "10.96.0.10")
	t.Setenv("PEER_POD_NODE_CLUSTER_DOMAIN", "tenant.example")
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEER_POD_NODE_CLIENT_CA_FILE", ca)
	path, err := prepareKubeletConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"10.96.0.10", "tenant.example", ca} {
		if !strings.Contains(string(contents), expected) {
			t.Errorf("generated kubelet config does not contain %q", expected)
		}
	}
}

func TestWaitForPathReportsProcessExit(t *testing.T) {
	managed := command("short-lived", "/bin/false")
	if err := start(managed); err != nil {
		t.Fatal(err)
	}
	err := waitForPath(context.Background(), filepath.Join(t.TempDir(), "missing"), time.Second, managed)
	var exited *processExitError
	if !errors.As(err, &exited) || exited.name != "short-lived" {
		t.Fatalf("unexpected wait error: %v", err)
	}
}

func TestTerminateStopsManagedProcess(t *testing.T) {
	managed := command("long-lived", "/bin/sleep", "30")
	if err := start(managed); err != nil {
		t.Fatal(err)
	}

	terminate([]*process{managed}, time.Second)
	select {
	case <-managed.done:
		if managed.cmd.ProcessState == nil {
			t.Fatalf("managed process was not reaped: %#v", managed.cmd.ProcessState)
		}
		if managed.err == nil {
			t.Fatal("terminated process unexpectedly reported a clean exit")
		}
	case <-time.After(time.Second):
		t.Fatal("managed process did not stop")
	}
}

func TestCleanupNetworkNamespacesRemovesStaleEntries(t *testing.T) {
	directory := t.TempDir()
	stale := filepath.Join(directory, "cni-stale")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupNetworkNamespaces(directory, func(string, int) error { return syscall.EINVAL }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale namespace entry still exists: %v", err)
	}
}

func TestPrepareKubeletConfigRejectsInvalidDNS(t *testing.T) {
	t.Setenv("PEER_POD_NODE_KUBELET_CONFIG", "")
	t.Setenv("PEER_POD_NODE_CLUSTER_DNS", "not-an-ip")
	if _, err := prepareKubeletConfig(t.TempDir()); err == nil {
		t.Fatal("invalid cluster DNS was accepted")
	}
}

func TestFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.so")
	if err := os.WriteFile(path, []byte("provider"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "5c4c1964340aca5b65393bbe9d3249cdd71be26665b3320ad694f034f2743283"
	if got != expected {
		t.Fatalf("unexpected plugin hash %q", got)
	}
}
