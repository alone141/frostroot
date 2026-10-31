#!/usr/bin/env python3
"""Boot a disk image in QEMU with OVMF and drive its serial console.

usage: disk_boot.py --image OVERLAY --code OVMF_CODE --vars OVMF_VARS --out DIR
                    [--credential NAME=VALUE] [--user USER --password PASSWORD]
                    [--expect refusal|login]

Starts qemu-system-x86_64 on the image, Secure Boot on, with the serial
console on this program's standard streams. With --expect login (the
default) it waits for the login prompt, logs in as the user with the
password, runs each check below, writes its output to DIR/<name>.txt, and
powers the machine off. With --expect refusal it waits for the firmware to
refuse the disk, which it prints on the serial line, and exits 0 when it
does; a login prompt is then a failure. The whole serial log is in
DIR/serial.log. Exits 1 when something expected does not appear in time.

There is no KVM in a container, so TCG runs the machine: a minute or two to
a login prompt. The password is passed as a hashed credential, so that the
first boot asks nothing on the screen and the serial line logs in.
"""
import argparse
import os
import re
import select
import subprocess
import sys
import time

ANSI = re.compile(rb"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[=>]")

# What the checks run as the user, in order, each under a name the shell
# scenario reads the output by. sudo needs no password: the recipe says so.
CHECKS = [
    ("sb-state", "sudo mokutil --sb-state"),
    ("system-running", "systemctl is-system-running --wait"),
    ("root-bytes", "df -B1 --output=size / | tail -n 1"),
    ("addresses", "ip -4 -o addr show"),
    ("dns", "getent hosts archive.ubuntu.com"),
    ("hostname", "hostname"),
    ("machine-id", "cat /etc/machine-id"),
    ("password", "sudo passwd -S student"),
    ("set-password", "systemctl show -p Result -p ActiveState frostroot-set-password.service"),
    ("host-keys", "ls /etc/ssh/ssh_host_ed25519_key"),
    ("units-failed", "systemctl --failed --no-legend"),
    ("partitions", "lsblk -b -n -o NAME,SIZE,TYPE /dev/vda"),
]


class Serial:
    """QEMU's serial console on a subprocess's pipes."""

    def __init__(self, argv, log):
        self.process = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.log = log
        self.buffer = b""

    def pump(self, seconds):
        """Reads what arrived within seconds, into the buffer and the log."""
        deadline = time.monotonic() + seconds
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            ready, _, _ = select.select([self.process.stdout], [], [], min(remaining, 0.2))
            if not ready:
                if self.process.poll() is not None:
                    return
                continue
            data = os.read(self.process.stdout.fileno(), 65536)
            if not data:
                return
            self.log.write(data)
            self.log.flush()
            self.buffer += data

    def wait_for(self, pattern, seconds, failures=()):
        """Waits for pattern in what arrives, returning the match, or None
        on a failure pattern or the deadline."""
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            text = ANSI.sub(b"", self.buffer)
            match = re.search(pattern, text)
            if match:
                self.buffer = self.buffer[match.end():] if match.end() <= len(self.buffer) else b""
                return match
            for failure in failures:
                if re.search(failure, text):
                    return None
            if self.process.poll() is not None:
                return None
            self.pump(1)
        return None

    def send(self, text):
        self.process.stdin.write(text.encode() + b"\n")
        self.process.stdin.flush()

    def stop(self, seconds):
        try:
            self.process.wait(seconds)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(10)


def qemu_argv(args):
    argv = [
        "qemu-system-x86_64",
        "-machine", "q35,smm=on,accel=kvm:tcg",
        "-cpu", "max", "-smp", "2", "-m", "2048",
        "-display", "none", "-monitor", "none", "-serial", "stdio", "-no-reboot",
        "-global", "driver=cfi.pflash01,property=secure,value=on",
        "-drive", "if=pflash,format=raw,readonly=on,file=" + args.code,
        "-drive", "if=pflash,format=raw,file=" + args.vars,
        "-drive", "file=" + args.image + ",format=qcow2,if=virtio",
        "-nic", "user,model=virtio-net-pci",
    ]
    if args.credential:
        argv += ["-smbios", "type=11,value=io.systemd.credential:" + args.credential]
    return argv


def run_checks(serial, args):
    prompt = rb"\$ $"
    for name, command in CHECKS:
        serial.buffer = b""
        # The console echoes the command, so the markers are typed broken in
        # two, and appear whole only in the output; the output itself may be
        # several lines, which (?s) lets the dot cross.
        serial.send(f"echo '@@''begin {name}'; {command}; echo '@@''end {name}'")
        match = serial.wait_for(rb"(?s)@@begin " + name.encode() + rb"\r?\n(.*?)\r?\n?@@end " + name.encode(), 120)
        output = ANSI.sub(b"", match.group(1)).replace(b"\r", b"") if match else b"(no answer within 120 s)\n"
        with open(os.path.join(args.out, name + ".txt"), "wb") as file:
            file.write(output + b"\n")
        print(f"  {name}: {output.decode('utf-8', 'replace').strip().splitlines()[-1] if output.strip() else '(nothing)'}")
        serial.wait_for(prompt, 10)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    parser.add_argument("--code", required=True)
    parser.add_argument("--vars", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--credential")
    parser.add_argument("--user", default="student")
    parser.add_argument("--password", default="")
    parser.add_argument("--expect", choices=["login", "refusal"], default="login")
    parser.add_argument("--boot-seconds", type=int, default=420)
    args = parser.parse_args()
    os.makedirs(args.out, exist_ok=True)
    log = open(os.path.join(args.out, "serial.log"), "wb")
    serial = Serial(qemu_argv(args), log)
    refused = (rb"Access Denied", rb"failed to load Boot", rb"Secure Boot violation")
    if args.expect == "refusal":
        match = serial.wait_for(rb"|".join(refused), args.boot_seconds, failures=(rb"login: ",))
        serial.process.kill()
        serial.stop(10)
        if match:
            print("  the firmware refused the disk: " + match.group(0).decode())
            return 0
        print("  the firmware did not refuse the disk within the time, or the machine booted")
        return 1
    if not serial.wait_for(rb"login: ", args.boot_seconds, failures=refused):
        print("  no login prompt within %d s" % args.boot_seconds)
        serial.process.kill()
        serial.stop(10)
        return 1
    serial.send(args.user)
    if not serial.wait_for(rb"Password: ", 60):
        print("  no password prompt")
        serial.process.kill()
        serial.stop(10)
        return 1
    serial.send(args.password)
    if not serial.wait_for(rb"\$ ", 120, failures=(rb"Login incorrect",)):
        print("  the login did not reach a shell")
        serial.process.kill()
        serial.stop(10)
        return 1
    print("  logged in as " + args.user)
    run_checks(serial, args)
    serial.send("sudo poweroff")
    serial.pump(60)
    serial.stop(60)
    return 0


if __name__ == "__main__":
    sys.exit(main())
