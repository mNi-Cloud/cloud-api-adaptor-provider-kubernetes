package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareDisksSkipsOverlayForDirectKernelBoot(t *testing.T) {
	command = func(name string, args ...string) error {
		if name != "mke2fs" {
			t.Fatalf("unexpected command %q", name)
		}
		if len(args) < 2 || args[len(args)-1] != "4M" {
			t.Fatalf("unexpected mke2fs args %q", args)
		}
		return os.WriteFile(args[len(args)-2], []byte("ext4"), 0o600)
	}
	t.Cleanup(func() { command = runCommand })
	stateDir := t.TempDir()
	imageDir := t.TempDir()
	configDir := t.TempDir()
	userData := []byte("#cloud-config")
	if err := os.WriteFile(filepath.Join(configDir, "userdata"), userData, 0o600); err != nil {
		t.Fatal(err)
	}
	image := []byte("not-a-real-image")
	if err := os.WriteFile(filepath.Join(imageDir, "disk.qcow2"), image, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := options{stateDir: stateDir, imageDir: imageDir, configDir: configDir, kernelPath: "/opt/podvm-runtime/vmlinuz"}
	if err := prepareDisks(opts, networkState{Address: "192.0.2.2/30", Gateway: "192.0.2.1", MAC: "02:00:00:00:00:08"}, "02:00:00:00:00:08"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "root.qcow2")); !os.IsNotExist(err) {
		t.Fatal("direct-kernel boot created a qcow2 overlay")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "cidata.img")); err != nil {
		t.Fatal("seed disk was not created: " + err.Error())
	}
}
