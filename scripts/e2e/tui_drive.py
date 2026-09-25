#!/usr/bin/env python3
"""Drive frostroot's full-screen form through a pseudo-terminal.

usage: tui_drive.py FROSTROOT [ARG...]

Runs "FROSTROOT init --force ARG..." in the current directory, in a 100x32
pseudo-terminal, the way a person at a terminal would: it walks the pages
with Enter, picks jq in the package picker and requests in the PyPI search,
and writes the recipe. It prints the screen as that person would have seen
it at each step, and exits 1 with the screen it gave up on when something
does not appear in time.

A terminal answers two questions Bubble Tea and lipgloss ask when they
start, the background colour (OSC 11) and the cursor position (DSR).
script(1) answers neither, and the form then never draws at all. This
answers both, and renders the output with a very small terminal emulator:
enough for Bubble Tea's renderer, which moves the cursor by lines and
rewrites whole lines. It is the only way anyone has seen the form draw
without a person in front of it.
"""
import fcntl
import os
import pty
import re
import select
import signal
import struct
import sys
import termios
import time

COLS, ROWS = 100, 32
ANSI = re.compile(rb"\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[=>]")
ENTER, SPACE, ESC = b"\r", b" ", b"\x1b"


class Screen:
    """Rows of text. It understands newline, carriage return, backspace,
    cursor movement, erase line, erase display and cursor home; colours are
    dropped."""

    def __init__(self):
        self.lines = [""] * ROWS
        self.row = 0
        self.col = 0

    def feed(self, data):
        text = data.decode("utf-8", "replace")
        i = 0
        while i < len(text):
            ch = text[i]
            if ch == "\x1b":
                m = re.match(r"\x1b\[([0-9;?]*)([A-Za-z])", text[i:])
                if m:
                    self.csi(m.group(1), m.group(2))
                    i += m.end()
                    continue
                m = re.match(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)", text[i:])
                i += m.end() if m else 2
                continue
            if ch == "\n":
                self.row = min(self.row + 1, ROWS - 1)
            elif ch == "\r":
                self.col = 0
            elif ch == "\b":
                self.col = max(self.col - 1, 0)
            elif ch >= " ":
                line = self.lines[self.row].ljust(self.col)
                self.lines[self.row] = line[: self.col] + ch + line[self.col + 1 :]
                self.col += 1
            i += 1

    def csi(self, params, final):
        parts = params.split(";")
        first = int(parts[0]) if parts[0].isdigit() else 0
        if final == "A":
            self.row = max(self.row - (first or 1), 0)
        elif final == "B":
            self.row = min(self.row + (first or 1), ROWS - 1)
        elif final == "C":
            self.col += first or 1
        elif final == "D":
            self.col = max(self.col - (first or 1), 0)
        elif final == "G":
            self.col = max(first - 1, 0)
        elif final == "H":
            self.row = int(parts[0]) - 1 if parts[0].isdigit() else 0
            self.col = int(parts[1]) - 1 if len(parts) > 1 and parts[1].isdigit() else 0
        elif final == "K":
            if first == 2:
                self.lines[self.row] = ""
            else:
                self.lines[self.row] = self.lines[self.row][: self.col]
        elif final == "J":
            if first in (2, 3):
                self.lines = [""] * ROWS
            else:
                self.lines[self.row] = self.lines[self.row][: self.col]
                for row in range(self.row + 1, ROWS):
                    self.lines[row] = ""

    def text(self):
        return "\n".join(line.rstrip() for line in self.lines).rstrip("\n")


class Session:
    def __init__(self, argv, env):
        self.screen = Screen()
        self.raw = b""
        self.closed = False
        pid, fd = pty.fork()
        if pid == 0:
            os.execvpe(argv[0], argv, env)
        self.pid, self.fd = pid, fd
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    def pump(self, seconds):
        """Reads output for about this long, answering the terminal's
        questions. False once the program has closed the terminal."""
        deadline = time.time() + seconds
        while time.time() < deadline and not self.closed:
            ready, _, _ = select.select([self.fd], [], [], 0.05)
            if not ready:
                continue
            try:
                data = os.read(self.fd, 65536)
            except OSError:
                data = b""
            if not data:
                self.closed = True
                break
            self.raw += data
            if b"\x1b]11;?" in data:
                os.write(self.fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
            if b"\x1b[6n" in data:
                os.write(self.fd, b"\x1b[1;1R")
            self.screen.feed(data)
        return not self.closed

    def wait_for(self, pattern, seconds):
        """Pumps until the screen matches the regular expression."""
        deadline = time.time() + seconds
        while time.time() < deadline:
            if re.search(pattern, self.screen.text()):
                return True
            if not self.pump(0.1):
                break
        return re.search(pattern, self.screen.text()) is not None

    def send(self, keys, settle=0.3):
        os.write(self.fd, keys)
        self.pump(settle)

    def type(self, text):
        for ch in text.encode():
            self.send(bytes([ch]), 0.05)
        self.pump(0.5)

    def show(self, title):
        print(f"\n----- {title} -----")
        print(self.screen.text())
        sys.stdout.flush()

    def exit_status(self, seconds):
        """The program's exit status, waiting this long for it; None when
        it is still running."""
        deadline = time.time() + seconds
        while time.time() < deadline:
            self.pump(0.2)
            pid, status = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                return os.waitstatus_to_exitcode(status)
        return None


def give_up(session, message):
    session.show("where it gave up")
    print(f"\nFAILED: {message}")
    try:
        os.kill(session.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    sys.exit(1)


def enter_until(session, pattern, presses, what):
    """Presses Enter until the screen matches, the way a person accepts
    each default in turn."""
    for _ in range(presses):
        if re.search(pattern, session.screen.text()):
            return
        session.send(ENTER, 0.5)
    if not re.search(pattern, session.screen.text()):
        give_up(session, f"never reached {what}")


def pick(session, name, what):
    """Types a name into the focused search, adds the row it lands on, and
    clears the query again."""
    session.type(name)
    session.pump(1.0)
    session.show(f"typed {name}")
    session.send(SPACE, 0.5)
    if not session.wait_for(r"chosen \(1\): " + re.escape(name) + r"\b", 5):
        give_up(session, f"Space did not add {name} to {what}")
    # A query stands after a pick, by design; Esc clears it, so that Enter
    # moves on instead of adding what was typed.
    session.send(ESC, 0.5)
    session.show(f"{name} chosen")


def main():
    if len(sys.argv) < 2:
        print(__doc__.split("\n\n")[1], file=sys.stderr)
        sys.exit(2)
    argv = [sys.argv[1], "init", "--force"] + sys.argv[2:]
    env = dict(os.environ, TERM="xterm-256color", LANG="C.UTF-8", COLUMNS=str(COLS), LINES=str(ROWS))
    started = time.time()
    session = Session(argv, env)

    if not session.wait_for(r"Image name", 20):
        give_up(session, "the form never drew its first page")
    session.show("the first page")

    # The picker's own key help, "add/remove", shows only while it has the
    # focus, and "Other packages" is the first picker on the page.
    enter_until(session, r"(?s)Other packages.*add/remove", 25, "the package picker")
    session.show("the package picker")
    if not session.wait_for(r"[0-9][0-9,]* packages", 300):
        give_up(session, "the release's package index never loaded")
    session.show(f"the package index, {time.time() - started:.0f} s after the start")
    pick(session, "jq", "the package picker")

    session.send(ENTER, 0.5)
    if not session.wait_for(r"[0-9][0-9,]* projects", 300):
        give_up(session, "PyPI's index never loaded in the Python field")
    session.show(f"PyPI's index, {time.time() - started:.0f} s after the start")
    pick(session, "requests", "the Python field")

    enter_until(session, r"Write frostroot\.toml\?", 15, "the summary")
    session.show("the summary")
    session.send(ENTER, 0.5)
    status = session.exit_status(30)
    tail = ANSI.sub(b"", session.raw[-2000:]).decode("utf-8", "replace").strip()
    print("\n----- what the terminal kept after the form closed -----")
    print(tail[-800:])
    if status is None:
        give_up(session, "the form did not exit after the recipe was confirmed")
    if status != 0:
        print(f"\nFAILED: frostroot init exited {status}")
        sys.exit(1)
    if not os.path.exists("frostroot.toml"):
        print("\nFAILED: frostroot init exited 0 and wrote no frostroot.toml")
        sys.exit(1)
    print(f"\nok: the recipe was written {time.time() - started:.0f} s after the start")


if __name__ == "__main__":
    main()
