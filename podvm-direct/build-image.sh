#!/bin/sh
set -eu

input_dir=${1:?input directory is required}
output_dir=${2:?output directory is required}
work_dir=/work
kata_dir=$work_dir/kata
guest_dir=$work_dir/generic

mkdir -p "$kata_dir" "$guest_dir" "$output_dir"
tar --zstd -xf "$input_dir/kernel.tar.zst" -C "$kata_dir"
tar --zstd -xf "$input_dir/rootfs.tar.zst" -C "$kata_dir"

kernel=$(find "$kata_dir" -type f -name 'vmlinux-[0-9]*' | head -n 1)
rootfs=$(find "$kata_dir" -type f -name 'kata-ubuntu-noble.image' | head -n 1)
test -n "$kernel"
test -n "$rootfs"
cp "$kernel" "$output_dir/vmlinux"
cp "$rootfs" "$output_dir/rootfs.img"

# The generic PodVM input is digest-pinned. Its root partition begins at sector
# 2099200 and contains the CoCo guest components that are not in Kata's base
# image. Extract only that partition; no privileged loop mount is required.
qemu-img convert -f qcow2 -O raw \
    "$input_dir/generic-podvm.qcow2" "$guest_dir/disk.raw"
dd if="$guest_dir/disk.raw" of="$guest_dir/rootfs.ext4" \
    bs=1M skip=1025 count=5120 status=none
truncate -s 5367643648 "$guest_dir/rootfs.ext4"
rm "$guest_dir/disk.raw"

dump_guest_file() {
    source_path=$1
    destination_path=$2
    debugfs -R "dump -p $source_path $destination_path" "$guest_dir/rootfs.ext4" >/dev/null 2>&1
    test -s "$destination_path"
}

dump_guest_file /usr/local/bin/agent-protocol-forwarder "$guest_dir/agent-protocol-forwarder"
dump_guest_file /usr/local/bin/process-user-data "$guest_dir/process-user-data"
dump_guest_file /usr/local/bin/confidential-data-hub "$guest_dir/confidential-data-hub"
dump_guest_file /usr/local/bin/attestation-agent "$guest_dir/attestation-agent"
dump_guest_file /etc/ocicrypt_config.json "$guest_dir/ocicrypt_config.json"
dump_guest_file /etc/aa-offline_fs_kbc-keys.json "$guest_dir/aa-offline_fs_kbc-keys.json"
dump_guest_file /etc/aa-offline_fs_kbc-resources.json "$guest_dir/aa-offline_fs_kbc-resources.json"
dump_guest_file /pause_bundle/config.json "$guest_dir/pause-config.json"
dump_guest_file /pause_bundle/umoci.json "$guest_dir/pause-umoci.json"
dump_guest_file \
    /pause_bundle/sha256_b42c514302e917881d20666a5990795df507ec14b2d79fdb1e41a619e66b77b6.mtree \
    "$guest_dir/pause.mtree"
dump_guest_file /pause_bundle/rootfs/pause "$guest_dir/pause"

# Kata's disk has a 250 MiB ext4 root partition at sector 6144. Work on the
# partition directly with debugfs, then copy it back into the bootable image.
dd if="$output_dir/rootfs.img" of="$work_dir/rootfs.ext4" \
    bs=512 skip=6144 count=512000 status=none

debugfs_write() {
    source_path=$1
    destination_path=$2
    mode=${3:-}
    debugfs -w -R "write $source_path $destination_path" "$work_dir/rootfs.ext4" >/dev/null 2>&1
    if [ -n "$mode" ]; then
        debugfs -w -R "set_inode_field $destination_path mode $mode" \
            "$work_dir/rootfs.ext4" >/dev/null 2>&1
    fi
}

for directory in /pause_bundle /pause_bundle/rootfs /etc/systemd/system /usr/local/bin; do
    debugfs -w -R "mkdir $directory" "$work_dir/rootfs.ext4" >/dev/null 2>&1 || true
done

debugfs_write "$guest_dir/agent-protocol-forwarder" /usr/local/bin/agent-protocol-forwarder 0100755
debugfs_write "$guest_dir/process-user-data" /usr/local/bin/process-user-data 0100755
debugfs_write "$guest_dir/confidential-data-hub" /usr/local/bin/confidential-data-hub 0100755
debugfs_write "$guest_dir/attestation-agent" /usr/local/bin/attestation-agent 0100755
debugfs_write /src/podvm-init /usr/local/bin/podvm-init 0100755
debugfs_write /src/agent-config.toml /etc/agent-config.toml 0100644
debugfs_write /src/podvm.service /etc/systemd/system/podvm.service 0100644
debugfs_write /src/podvm.target /etc/systemd/system/podvm.target 0100644
debugfs_write "$guest_dir/ocicrypt_config.json" /etc/ocicrypt_config.json 0100644
debugfs_write "$guest_dir/aa-offline_fs_kbc-keys.json" /etc/aa-offline_fs_kbc-keys.json 0100644
debugfs_write "$guest_dir/aa-offline_fs_kbc-resources.json" /etc/aa-offline_fs_kbc-resources.json 0100644
debugfs_write "$guest_dir/pause-config.json" /pause_bundle/config.json 0100644
debugfs_write "$guest_dir/pause-umoci.json" /pause_bundle/umoci.json 0100644
debugfs_write "$guest_dir/pause.mtree" \
    /pause_bundle/sha256_b42c514302e917881d20666a5990795df507ec14b2d79fdb1e41a619e66b77b6.mtree \
    0100644
debugfs_write "$guest_dir/pause" /pause_bundle/rootfs/pause 0100755

e2fsck -pf "$work_dir/rootfs.ext4"
dd if="$work_dir/rootfs.ext4" of="$output_dir/rootfs.img" \
    bs=512 seek=6144 conv=notrunc status=none
chmod 0644 "$output_dir/vmlinux" "$output_dir/rootfs.img"
