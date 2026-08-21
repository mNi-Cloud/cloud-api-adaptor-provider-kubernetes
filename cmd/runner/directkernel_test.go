package main

import (
	"strings"
	"testing"
)

func TestCloudHypervisorArgsDirectKernelBootOmitsFirmware(t *testing.T) {
	args := cloudHypervisorArgs(options{cpus: 1, memoryMiB: 512, firmware: "/usr/share/cloud-hypervisor/CLOUDHV.fd", kernelPath: "/opt/podvm-runtime/vmlinuz", initramfsPath: "/opt/podvm-runtime/kata-containers-initrd.img", rootfsPath: "/opt/podvm-runtime/rootfs.raw"}, "/sock", "/root.qcow2", "/cidata.img", "02:00:00:00:00:08")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--kernel /opt/podvm-runtime/vmlinuz", "--initramfs /opt/podvm-runtime/kata-containers-initrd.img", "--cmdline", "path=/opt/podvm-runtime/rootfs.raw,image_type=raw", "path=/cidata.img,readonly=on,image_type=raw"} {
		if !strings.Contains(joined, want) {
			t.Error("direct-kernel args do not contain: " + want)
		}
	}
	if strings.Contains(joined, "--firmware") {
		t.Errorf("direct-kernel args still pass --firmware")
	}
}

func TestCloudHypervisorArgsDirectKernelDefaultsCmdlineAndSeedOnly(t *testing.T) {
	args := cloudHypervisorArgs(options{cpus: 1, memoryMiB: 512, kernelPath: "/opt/podvm-runtime/vmlinuz"}, "/sock", "/root.qcow2", "/cidata.img", "02:00:00:00:00:08")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--cmdline "+directKernelParams) {
		t.Errorf("direct-kernel args do not default the cmdline")
	}
	if strings.Contains(joined, "rootfs.raw") {
		t.Errorf("direct-kernel args unexpectedly include a rootfs disk")
	}
	if !strings.Contains(joined, "path=/cidata.img,readonly=on,image_type=raw") {
		t.Errorf("direct-kernel args do not include the seed disk")
	}
}

func TestCloudHypervisorArgsDirectKernelHonorsCustomParams(t *testing.T) {
	args := cloudHypervisorArgs(options{cpus: 1, memoryMiB: 512, kernelPath: "/opt/podvm-runtime/vmlinuz", kernelParams: "console=ttyS0 root=/dev/vda rw panic=1"}, "/sock", "/root.qcow2", "/cidata.img", "02:00:00:00:00:08")
	joined := strings.Join(args, " ")
	want := "--cmdline console=ttyS0 root=/dev/vda rw panic=1"
	if !strings.Contains(joined, want) {
		t.Error("direct-kernel args do not honor --kernel-params")
	}
}
