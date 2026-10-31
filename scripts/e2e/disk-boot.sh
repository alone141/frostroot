#!/usr/bin/env bash
# proves: a disk image boots in QEMU with OVMF and Microsoft's keys, Secure Boot on: the password set from a hashed credential, a login on the serial line, systemd running with nothing failed, the root grown to the disk, DHCP and DNS, the host name, a machine id of its own, the SSH host keys made at the first boot; and with the snakeoil variables the firmware refuses it
# needs: the network, mmdebstrap, ubuntu-keyring, bubblewrap, user namespaces or root, qemu-system-x86_64, qemu-img, ovmf, openssl, python3
# takes: about 15 minutes: a disk build, and two boots without KVM
#
# The README's check of a disk image, which only a firmware and a kernel can
# make. QEMU boots the image from an overlay twice the image's size, so
# that systemd-repart has somewhere to grow the root to, with OVMF's
# variables that enrol Microsoft's keys and Secure Boot on; the user's
# password reaches the machine as the hashed credential a hypervisor would
# pass, so the first boot asks nothing on the screen and the serial line
# logs in. disk_boot.py drives the console; what the machine answered is in
# the lab's boot/ directory. The second boot uses OVMF's snakeoil variables,
# whose keys signed nothing the disk holds, and must be refused. A real PC
# is the owner's check. E2E_OVMF_DIR names where the firmware files are.
source "$(dirname "$0")/lib.sh"

E2E_TARGET=disk
e2e_begin disk-boot
e2e_require_bootstrap
e2e_require qemu-system-x86_64 qemu-img openssl python3
ovmf=${E2E_OVMF_DIR:-/usr/share/OVMF}
for file in OVMF_CODE_4M.secboot.fd OVMF_VARS_4M.ms.fd OVMF_VARS_4M.snakeoil.fd; do
	if [ ! -f "$ovmf/$file" ]; then
		echo "SKIP disk-boot: needs $ovmf/$file (sudo apt install ovmf, or E2E_OVMF_DIR=...)"
		exit 4
	fi
done
e2e_build_frostroot
lab=$LAB/lab
image=$lab/$(e2e_output e2e-disk)
e2e_recipe "$lab" e2e-disk "$E2E_RELEASE" "git openssh-server"

e2e_run build "$lab" "$FROSTROOT" build --plain
e2e_expect_status 0 build
e2e_check "the build says how to write the image and that the first boot asks the password" e2e_contains "$LAB/build.out" "sudo dd if="
e2e_check "the lock records the disk's layout" e2e_contains "$lab/frostroot.lock" "[disk]"
e2e_check "the image is there" test -s "$image"

password="e2e-$(openssl rand -hex 6)"
hash=$(openssl passwd -6 "$password")
qemu-img create -q -f qcow2 -b "$image" -F raw "$LAB/overlay.qcow2" 8G || e2e_abort "qemu-img could not make the overlay"
cp "$ovmf/OVMF_VARS_4M.ms.fd" "$LAB/vars-ms.fd"
cp "$ovmf/OVMF_VARS_4M.snakeoil.fd" "$LAB/vars-snakeoil.fd"

e2e_run boot "$LAB" python3 "$e2eRepo/scripts/e2e/disk_boot.py" --image "$LAB/overlay.qcow2" \
	--code "$ovmf/OVMF_CODE_4M.secboot.fd" --vars "$LAB/vars-ms.fd" --out "$LAB/boot" \
	--credential "passwd.hashed-password.student=$hash" --user student --password "$password"
e2e_expect_status 0 boot
boot=$LAB/boot
e2e_check "Secure Boot is on" e2e_contains "$boot/sb-state.txt" "SecureBoot enabled"
e2e_check "systemd is running" e2e_matches "$boot/system-running.txt" '^running$'
e2e_check "no unit failed" e2e_lacks_match "$boot/units-failed.txt" '\.service'
e2e_check "the root grew to fill the 8G disk" test "$(tr -dc 0-9 < "$boot/root-bytes.txt")" -gt 6000000000
e2e_check "the machine got an address by DHCP" e2e_contains "$boot/addresses.txt" "10.0.2.15"
e2e_check "and resolves names" e2e_contains "$boot/dns.txt" "archive.ubuntu.com"
e2e_check "the host name is the image's" e2e_matches "$boot/hostname.txt" '^e2e-disk$'
e2e_check "the machine has an id of its own" e2e_matches "$boot/machine-id.txt" '^[0-9a-f]{32}$'
e2e_check "the password was set from the credential" e2e_matches "$boot/password.txt" '^student P '
e2e_check "by the set-password unit, which ran and succeeded" e2e_contains "$boot/set-password.txt" "Result=success"
e2e_check "the SSH host keys were made at the first boot" e2e_contains "$boot/host-keys.txt" "/etc/ssh/ssh_host_ed25519_key"
e2e_check "the second partition is the root, grown" e2e_matches "$boot/partitions.txt" 'vda2 +[0-9]{10,} +part'

e2e_run refusal "$LAB" python3 "$e2eRepo/scripts/e2e/disk_boot.py" --image "$LAB/overlay.qcow2" \
	--code "$ovmf/OVMF_CODE_4M.secboot.fd" --vars "$LAB/vars-snakeoil.fd" --out "$LAB/refusal" --expect refusal --boot-seconds 180
e2e_check "firmware with other keys refuses the disk" test "$e2eStatus" -eq 0
e2e_end
