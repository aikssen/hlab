package proxmox

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeStream is an in-memory termStream: recv returns queued output chunks in
// order (then recvErr, defaulting to io.EOF), and send records what was typed.
// It lets the console state machine be driven with no network.
type fakeStream struct {
	recvs   [][]byte
	ri      int
	sends   []string
	recvErr error
}

func (f *fakeStream) send(s string) error { f.sends = append(f.sends, s); return nil }

func (f *fakeStream) recv() ([]byte, error) {
	if f.ri >= len(f.recvs) {
		if f.recvErr != nil {
			return nil, f.recvErr
		}
		return nil, io.EOF
	}
	b := f.recvs[f.ri]
	f.ri++
	return b, nil
}

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"plain":                  "plain",
		"\x1b[31mred\x1b[0m":     "red",
		"\x1b[2J\x1b[Hcleared":   "cleared",
		"a\rb":                   "ab",    // carriage returns dropped
		"\x1b]0;title\x07shell":  "shell", // OSC terminated by BEL
		"\x1b]0;t\x1b\\body":     "body",  // OSC terminated by ST
		"root@ct:~# ":            "root@ct:~# ",
		"login\x1b[6nincomplete": "loginincomplete",
		// Observed live (PVE 9.x container getty): the size probe — park the cursor
		// far bottom-right, then DSR — must strip to nothing.
		"\x1b[32766;32766H\x1b[6n": "",
		// OSC with semicolon-separated fields, from the live console stream.
		"\x1b]3008;start=1751500000;user=root;host=ct\x07debian login: ": "debian login: ",
	}
	for in, want := range cases {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEndsWith(t *testing.T) {
	cases := []struct {
		text, suffix string
		want         bool
	}{
		{"debian login: ", "login:", true},
		{"Password: ", "password:", true}, // case-insensitive
		{"PASSWORD:", "password:", true},
		{"root@ct:~# ", "login:", false},
		{"some login: banner\nlogin: ", "login:", true}, // trailing prompt wins
		{"", "login:", false},
	}
	for _, c := range cases {
		if got := endsWith(c.text, c.suffix); got != c.want {
			t.Errorf("endsWith(%q, %q) = %v, want %v", c.text, c.suffix, got, c.want)
		}
	}
}

func TestHasShellPrompt(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"root@ct:~# ", true},
		{"\nroot@ct:~#", true},
		{"debian login: ", false},
		{"Password: ", false},
		{"# a comment line in the MOTD\nroot@ct:~# ", true}, // last line decides
		{"welcome # to the box\n", false},                   // a mid-MOTD '#' must not match
		{"", false},
	}
	for _, c := range cases {
		if got := hasShellPrompt(c.text); got != c.want {
			t.Errorf("hasShellPrompt(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestNewSentinel locks in the anti-echo trick: the marker hlab scans for must NOT
// appear verbatim in the fragment it types (so the tty echoing the command back
// can't be mistaken for the command's output), yet removing the empty-string
// concatenation must reproduce the marker.
func TestNewSentinel(t *testing.T) {
	marker, sentinel := newSentinel()
	if !strings.HasPrefix(marker, "HLAB_OK_") {
		t.Errorf("marker %q missing HLAB_OK_ prefix", marker)
	}
	if strings.Contains(sentinel, marker) {
		t.Errorf("typed sentinel %q must not contain the marker literal %q", sentinel, marker)
	}
	if got := strings.ReplaceAll(sentinel, `"`, ""); got != marker {
		t.Errorf("sentinel with quotes removed = %q, want the marker %q", got, marker)
	}
}

func TestDriveHappyPath(t *testing.T) {
	fs := &fakeStream{recvs: [][]byte{
		[]byte("\r\ndebian login: "),
		[]byte("root\r\nPassword: "),
		[]byte("\r\nLinux ct 6.x\r\nroot@ct:~# "),
		[]byte("run-it && echo ...\r\nMARK123\r\nroot@ct:~# "),
	}}
	err := drive(fs, ConsoleLogin{User: "root", Password: "s3cret"}, "run-it", "MARK123")
	if err != nil {
		t.Fatalf("drive() = %v, want nil", err)
	}
	// The username and password are each prefixed with Ctrl-U (killLine) to clear
	// any stray input the tty may already hold before the credential is typed.
	want := []string{"\n", killLine + "root\n", killLine + "s3cret\n", "run-it\n", "exit\n"}
	if strings.Join(fs.sends, "|") != strings.Join(want, "|") {
		t.Errorf("sends = %v, want %v", fs.sends, want)
	}
}

func TestDriveAlreadyLoggedIn(t *testing.T) {
	// The console already sits at a shell (no login/password prompt): the state
	// machine must skip straight to running the command.
	fs := &fakeStream{recvs: [][]byte{
		[]byte("\r\nroot@ct:~# "),
		[]byte("run-it\r\nMARK123\r\nroot@ct:~# "),
	}}
	if err := drive(fs, ConsoleLogin{User: "root", Password: "x"}, "run-it", "MARK123"); err != nil {
		t.Fatalf("drive() = %v, want nil", err)
	}
	want := []string{"\n", "run-it\n", "exit\n"}
	if strings.Join(fs.sends, "|") != strings.Join(want, "|") {
		t.Errorf("sends = %v, want %v (login must be skipped)", fs.sends, want)
	}
}

// gettyStream models the agetty behavior observed live on PVE 9.x: it emits
// only cursor-position probes (\x1b[32766;32766H\x1b[6n) and NEVER a "login:"
// prompt until the client answers with a Cursor Position Report — a silent
// client leaves getty restarting in a loop forever. recv pops from a queue;
// send appends the scripted next output, so the login prompt appears if and
// only if drive answers the probe.
type gettyStream struct {
	sends []string
	queue [][]byte
}

func (g *gettyStream) send(s string) error {
	g.sends = append(g.sends, s)
	switch s {
	case cursorReport:
		g.queue = append(g.queue, []byte("\r\ndebian login: "))
	case killLine + "root\n":
		g.queue = append(g.queue, []byte("root\r\nPassword: "))
	case killLine + "pw\n":
		g.queue = append(g.queue, []byte("\r\nroot@ct:~# "))
	case "run-it\n":
		g.queue = append(g.queue, []byte("run-it\r\nMARK123\r\nroot@ct:~# "))
	}
	return nil
}

func (g *gettyStream) recv() ([]byte, error) {
	if len(g.queue) == 0 {
		return nil, io.EOF // nothing scripted — the probe went unanswered
	}
	b := g.queue[0]
	g.queue = g.queue[1:]
	return b, nil
}

// TestDriveAnswersCursorProbe reproduces the live failure: getty probes the
// terminal size before printing "login:", so drive must reply with a CPR (as
// framed input) — otherwise the login prompt never appears and drive times out.
func TestDriveAnswersCursorProbe(t *testing.T) {
	gs := &gettyStream{queue: [][]byte{
		[]byte("\x1b[32766;32766H\x1b[6n"), // getty's size probe, verbatim from the live stream
	}}
	if err := drive(gs, ConsoleLogin{User: "root", Password: "pw"}, "run-it", "MARK123"); err != nil {
		t.Fatalf("drive() = %v, want nil (the probe must be answered so login can proceed)", err)
	}
	want := []string{"\n", cursorReport, killLine + "root\n", killLine + "pw\n", "run-it\n", "exit\n"}
	if strings.Join(gs.sends, "|") != strings.Join(want, "|") {
		t.Errorf("sends = %q, want %q", gs.sends, want)
	}
}

// TestDriveAnswersSplitAndRepeatedProbes covers two real-world shapes seen
// BEFORE the login prompt: a probe split across two websocket frames (the carry
// must reassemble it) and repeated probes in one frame — each pre-prompt
// occurrence gets its own CPR reply. It also asserts the opposite for the
// post-prompt case: the probe that rides along AFTER login is NOT answered
// (see TestDriveIgnoresProbeAfterPrompt), because a CPR typed then would be
// echoed into the tty as garbage input.
func TestDriveAnswersSplitAndRepeatedProbes(t *testing.T) {
	fs := &fakeStream{recvs: [][]byte{
		[]byte("\x1b["),                // probe split across frames…
		[]byte("6n"),                   // …completed here → 1 reply
		[]byte("\x1b[6n\x1b[6n"),       // two probes in one frame → 2 replies
		[]byte("\r\ndebian login: "),   // then getty finally prompts
		[]byte("root\r\nPassword: "),   //
		[]byte("\r\nroot@ct:~# "),      //
		[]byte("\x1b[6nMARK123\r\n# "), // a post-login probe rides along — must NOT be answered
	}}
	if err := drive(fs, ConsoleLogin{User: "root", Password: "pw"}, "run-it", "MARK123"); err != nil {
		t.Fatalf("drive() = %v, want nil", err)
	}
	got := strings.Join(fs.sends, "|")
	want := strings.Join([]string{
		"\n", cursorReport, cursorReport, cursorReport,
		killLine + "root\n", killLine + "pw\n", "run-it\n", "exit\n",
	}, "|")
	if got != want {
		t.Errorf("sends = %q, want %q", got, want)
	}
}

// TestDriveIgnoresProbeAfterPrompt isolates behavior 1: once a login prompt has
// appeared, a subsequent cursor-position probe is left UNANSWERED — answering it
// would echo the CPR reply straight into the username field (observed live) and
// fail the login. Before the prompt the probe is still answered.
func TestDriveIgnoresProbeAfterPrompt(t *testing.T) {
	fs := &fakeStream{recvs: [][]byte{
		[]byte("\x1b[6n"),            // pre-prompt probe → answered
		[]byte("\r\ndebian login: "), // prompt appears → stop answering probes
		[]byte("\x1b[6n"),            // post-prompt probe → must be ignored
		[]byte("root\r\nPassword: "),
		[]byte("\x1b[6n\r\nroot@ct:~# "), // another post-prompt probe → ignored
		[]byte("run-it\r\nMARK123\r\n# "),
	}}
	if err := drive(fs, ConsoleLogin{User: "root", Password: "pw"}, "run-it", "MARK123"); err != nil {
		t.Fatalf("drive() = %v, want nil", err)
	}
	got := strings.Join(fs.sends, "|")
	want := strings.Join([]string{
		"\n", cursorReport, killLine + "root\n", killLine + "pw\n", "run-it\n", "exit\n",
	}, "|")
	if got != want {
		t.Errorf("sends = %q, want %q (only the pre-prompt probe should be answered)", got, want)
	}
}

// TestDriveLoginIncorrect covers behavior 3's give-up path: the first "Login
// incorrect" is retried once, and only a SECOND failure surfaces the bad-password
// error (a single failure would be silently retried).
func TestDriveLoginIncorrect(t *testing.T) {
	fs := &fakeStream{recvs: [][]byte{
		[]byte("login: "),
		[]byte("Password: "),
		[]byte("\r\nLogin incorrect\r\n"), // first failure → retry
		[]byte("debian login: "),
		[]byte("Password: "),
		[]byte("\r\nLogin incorrect\r\n"), // second failure → give up
	}}
	err := drive(fs, ConsoleLogin{User: "root", Password: "wrong"}, "run-it", "MARK123")
	if err == nil {
		t.Fatal("drive() = nil, want an error on incorrect login")
	}
	if !strings.Contains(err.Error(), "incorrect password") {
		t.Errorf("error should explain the bad password, got: %v", err)
	}
	// Two full login attempts were made (username typed twice) before giving up.
	if n := strings.Count(strings.Join(fs.sends, "|"), killLine+"root\n"); n != 2 {
		t.Errorf("username typed %d times, want 2 (one retry after the first failure)", n)
	}
}

// TestDriveRetriesOnLoginIncorrect covers behavior 3's happy path: a first "Login
// incorrect" (e.g. a CPR reply that leaked into the username field) is retried,
// and the second attempt logs in and runs the command.
func TestDriveRetriesOnLoginIncorrect(t *testing.T) {
	fs := &fakeStream{recvs: [][]byte{
		[]byte("\r\ndebian login: "),
		[]byte("Password: "),
		[]byte("\r\nLogin incorrect\r\n"), // first attempt fails → retry once
		[]byte("\r\ndebian login: "),      // getty re-prompts
		[]byte("Password: "),
		[]byte("\r\nroot@ct:~# "), // logged in this time
		[]byte("run-it\r\nMARK123\r\nroot@ct:~# "),
	}}
	if err := drive(fs, ConsoleLogin{User: "root", Password: "pw"}, "run-it", "MARK123"); err != nil {
		t.Fatalf("drive() = %v, want nil (the retry should succeed)", err)
	}
	want := []string{
		"\n",
		killLine + "root\n", killLine + "pw\n", // first attempt
		killLine + "root\n", killLine + "pw\n", // retry
		"run-it\n", "exit\n",
	}
	if strings.Join(fs.sends, "|") != strings.Join(want, "|") {
		t.Errorf("sends = %v, want %v", fs.sends, want)
	}
}

func TestDriveConnectionClosed(t *testing.T) {
	// The websocket drops before the sentinel arrives: drive must report it (with
	// the buffered output), not hang or succeed.
	fs := &fakeStream{
		recvs:   [][]byte{[]byte("root@ct:~# "), []byte("run-it\r\n")},
		recvErr: io.EOF,
	}
	err := drive(fs, ConsoleLogin{User: "root", Password: "x"}, "run-it", "MARK123")
	if err == nil {
		t.Fatal("drive() = nil, want an error when the console closes early")
	}
	if !strings.Contains(err.Error(), "console closed") {
		t.Errorf("error should note the console closed, got: %v", err)
	}
}

func TestDriveRecvError(t *testing.T) {
	fs := &fakeStream{recvErr: errors.New("read timeout")}
	err := drive(fs, ConsoleLogin{User: "root", Password: "x"}, "run-it", "MARK123")
	if err == nil || !strings.Contains(err.Error(), "read timeout") {
		t.Errorf("drive() = %v, want the underlying read error surfaced", err)
	}
}

// TestVncWebsocketURL pins the pure URL transform: the REST scheme is swapped to
// the websocket one (https→wss, http→ws) and the port/ticket are query-escaped.
func TestVncWebsocketURL(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{
			"https://pve.example:8006/api2/json",
			"wss://pve.example:8006/api2/json/nodes/pve2/lxc/6101/vncwebsocket?port=5900&vncticket=PVE%3Aabc",
		},
		{
			"http://pve.example:8006/api2/json",
			"ws://pve.example:8006/api2/json/nodes/pve2/lxc/6101/vncwebsocket?port=5900&vncticket=PVE%3Aabc",
		},
	}
	for _, c := range cases {
		client := New(c.base, "t", "s", false)
		got := client.vncWebsocketURL("pve2", 6101, "5900", "PVE:abc")
		if got != c.want {
			t.Errorf("vncWebsocketURL(base=%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestTail(t *testing.T) {
	if got := tail("short", 10); got != "short" {
		t.Errorf("tail(short) = %q, want unchanged", got)
	}
	got := tail("0123456789abc", 3)
	if got != "…abc" {
		t.Errorf("tail truncation = %q, want %q", got, "…abc")
	}
}
