package proxmox

// Console-based command execution over the Proxmox VE termproxy websocket.
//
// The Proxmox web UI's "Console" for an LXC container drives the same two-step
// API this file automates:
//
//  1. POST /nodes/{node}/lxc/{vmid}/termproxy → {user, ticket, port}
//  2. a websocket to
//     /api2/json/nodes/{node}/lxc/{vmid}/vncwebsocket?port={port}&vncticket={ticket}
//
// hlab uses it for exactly one thing: seeding an SSH key into a container that
// was created with NO key. Such a container is unreachable over SSH (sshd's
// PermitRootLogin prohibit-password refuses the root password), so the key has
// to be injected the only way in — the console, which does accept the root
// password. After the key lands, ordinary SSH works again.
//
// The websocket protocol (confirmed against pve-xtermjs src/www/main.js and a
// Go reimplementation, luth.io/blog/2022/02/recreating-proxmox-ve-xtermjs-in-go):
//   - after the socket opens the client sends "<user>:<ticket>\n" (binary); the
//     server replies "OK".
//   - client → server data is framed as "0:<bytelen>:<data>".
//   - resize is "1:<cols>:<rows>:" and a keepalive ping is the single byte "2".
//   - server → client is RAW terminal output (no framing), the first frame being
//     the "OK" handshake ack.
//
// The login/prompt/sentinel state machine (drive) is deliberately split from the
// websocket transport (termStream) so it can be unit-tested against an in-memory
// stream with no network.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ConsoleLogin is the tty credential the console state machine logs in with. For
// an LXC container this is always root plus the container's stored root password.
type ConsoleLogin struct {
	User     string
	Password string
}

// consoleDeadline bounds the whole console exchange (dial + login + script). A
// container getty is quick, but a slow first-boot or a laggy websocket shouldn't
// hang hlab forever.
const consoleDeadline = 60 * time.Second

// termCols/termRows is the terminal size hlab declares on the console. The
// initial resize frame ("1:<cols>:<rows>:" — cols first, per pve-xtermjs
// main.js) and the cursor-position report below must describe the SAME size,
// or getty computes a nonsense geometry.
const (
	termCols = 80
	termRows = 24
)

// cursorProbe is the DSR (Device Status Report) sequence agetty emits to
// discover the terminal size: it parks the cursor at the far bottom-right
// (\x1b[32766;32766H) and asks where it actually ended up. A real terminal —
// and xterm.js in the web console — answers with a Cursor Position Report; a
// client that stays silent makes getty give up WITHOUT ever printing "login:",
// and systemd restarts it in a loop. So the state machine answers every probe
// it sees BEFORE a prompt appears; once a login/password prompt is up the tty
// echoes input, so a CPR reply typed then would pollute the credential field
// (see promptSeen in drive), and answering stops.
const cursorProbe = "\x1b[6n"

// cursorReport is the CPR answer to cursorProbe — "\x1b[<row>;<col>R", i.e.
// "the cursor is at row 24, column 80" — derived from the same termRows/termCols
// the resize frame declares so the two can never disagree. It is typed as
// ordinary terminal input.
var cursorReport = fmt.Sprintf("\x1b[%d;%dR", termRows, termCols)

// killLine is the tty "kill line" control (Ctrl-U): it clears the current input
// line. hlab types it right before the username and the password so that any
// stray bytes already sitting in the login field — e.g. a CPR reply the getty
// echoed while a prompt was already up — are wiped before the credential is
// entered, instead of polluting it into a "Login incorrect".
const killLine = "\x15"

// termStream is the minimal duplex byte stream the console state machine drives.
// send writes one burst of input to type at the tty; recv returns the next chunk
// of raw terminal output. The websocket implementation frames sends as
// "0:<len>:<data>"; tests use an in-memory fake.
type termStream interface {
	send(data string) error
	recv() ([]byte, error)
}

// ConsoleExec logs into an LXC container over the Proxmox console websocket and
// runs script (its lines joined with " && "), returning nil only when a success
// sentinel is observed on stdout. It is the one way to configure a container that
// has no SSH access (a keyless container refuses root password auth over SSH but
// accepts it on the console). A 403 from termproxy means the API token's role is
// missing the VM.Console privilege — surfaced with an actionable hint.
func (c *Client) ConsoleExec(node string, vmid int, script []string, login ConsoleLogin) error {
	ctx, cancel := context.WithTimeout(context.Background(), consoleDeadline)
	defer cancel()

	user, ticket, port, err := c.termProxy(ctx, node, vmid)
	if err != nil {
		return err
	}

	wsURL := c.vncWebsocketURL(node, vmid, port, ticket)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient:   c.wsHTTPClient(),
		HTTPHeader:   http.Header{"Authorization": {c.authHeader()}},
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		return fmt.Errorf("opening the console websocket failed: %w", err)
	}
	// A container's MOTD/getty output easily exceeds the 32 KiB default.
	conn.SetReadLimit(1 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Authenticate the session: "<user>:<ticket>\n" as a raw binary frame.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte(user+":"+ticket+"\n")); err != nil {
		return fmt.Errorf("console authentication failed: %w", err)
	}
	stream := &wsStream{ctx: ctx, conn: conn}
	// Consume the handshake ack ("OK") so the auth is fully processed before the
	// state machine nudges the tty (an input frame sent too early is dropped, and
	// a getty only re-prints its login prompt in response to a keypress).
	if _, err := stream.recv(); err != nil {
		return fmt.Errorf("console handshake failed: %w", err)
	}
	// Declare the terminal size ("1:<cols>:<rows>:") right after the handshake,
	// like the web console does on connect — getty/shell size probes are answered
	// with a matching cursor report by the state machine (see cursorProbe).
	if err := conn.Write(ctx, websocket.MessageBinary,
		fmt.Appendf(nil, "1:%d:%d:", termCols, termRows)); err != nil {
		return fmt.Errorf("console resize failed: %w", err)
	}

	marker, sentinel := newSentinel()
	command := strings.Join(script, " && ") + " && echo " + sentinel
	return drive(stream, login, command, marker)
}

// termProxy requests a console ticket for a container, returning the login user,
// the one-time ticket and the assigned port.
func (c *Client) termProxy(ctx context.Context, node string, vmid int) (user, ticket, port string, err error) {
	path := fmt.Sprintf("%s/nodes/%s/lxc/%d/termproxy", c.baseURL, node, vmid)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Authorization", c.authHeader())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		return "", "", "", fmt.Errorf("Proxmox denied console access (403): the API token role needs the VM.Console privilege — grant it with `pveum role modify HLab --privs \"VM.Console\" --append 1`")
	}
	if resp.StatusCode >= 300 {
		return "", "", "", fmt.Errorf("proxmox API termproxy: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var r struct {
		Data struct {
			User   string `json:"user"`
			Ticket string `json:"ticket"`
			Port   any    `json:"port"` // Proxmox returns a number; render it flexibly
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", "", "", fmt.Errorf("parsing termproxy response: %w", err)
	}
	port = strings.TrimSuffix(fmt.Sprintf("%v", r.Data.Port), ".0") // float64(5900) → "5900"
	if r.Data.Ticket == "" || port == "" {
		return "", "", "", fmt.Errorf("termproxy returned no ticket/port")
	}
	return r.Data.User, r.Data.Ticket, port, nil
}

// vncWebsocketURL builds the wss:// URL for the container's console stream,
// mirroring the REST base but swapping the scheme to the websocket one.
func (c *Client) vncWebsocketURL(node string, vmid int, port, ticket string) string {
	base := c.baseURL
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return fmt.Sprintf("%s/nodes/%s/lxc/%d/vncwebsocket?port=%s&vncticket=%s",
		base, node, vmid, url.QueryEscape(port), url.QueryEscape(ticket))
}

// wsHTTPClient returns an http.Client for the websocket handshake. It must NOT
// carry a Timeout (that would apply to the whole connection lifetime and kill the
// long-lived console stream), and it honors the insecure flag like the REST client.
func (c *Client) wsHTTPClient() *http.Client {
	tr := &http.Transport{}
	if c.insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Transport: tr}
}

// wsStream adapts a coder/websocket connection to termStream, framing sends as
// "0:<bytelen>:<data>" and returning raw output on recv.
type wsStream struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (w *wsStream) send(data string) error {
	frame := fmt.Sprintf("0:%d:%s", len(data), data)
	return w.conn.Write(w.ctx, websocket.MessageBinary, []byte(frame))
}

func (w *wsStream) recv() ([]byte, error) {
	_, data, err := w.conn.Read(w.ctx)
	return data, err
}

// console state machine phases.
type consolePhase int

const (
	phaseLogin    consolePhase = iota // waiting for the "login:" prompt (or an already-open shell)
	phasePassword                     // waiting for the "Password:" prompt
	phaseShell                        // logged in; waiting for a shell prompt to run the command
	phaseMarker                       // command sent; waiting for the success sentinel
)

// drive runs the login → command → sentinel exchange over s. It logs in as
// login.User/login.Password, runs command once a shell prompt appears, and
// returns nil only when marker is seen in the output (success). Every wait is
// bounded by the stream's own deadline (the websocket ctx); on any error the
// accumulated output tail is attached for diagnosis. Split from the transport so
// it is unit-testable against an in-memory termStream.
//
// Besides prompt matching, drive answers the tty's cursor-position probes
// (cursorProbe → cursorReport) until a prompt appears: agetty won't print a
// login prompt at all until its size probe is answered, so this is a hard
// requirement, not a nicety — but once the prompt is up, answering would echo
// garbage into the login field, so it stops (promptSeen).
func drive(s termStream, login ConsoleLogin, command, marker string) error {
	phase := phaseLogin

	var raw strings.Builder // full raw output, for error reporting
	var win strings.Builder // ANSI-stripped window since the last phase change, for matching
	var probeCarry string   // tail of the previous chunk, so a probe split across frames still matches
	promptSeen := false     // a login/password prompt has appeared → stop answering size probes
	retried := false        // a "Login incorrect" has already been retried once

	// Nudge the tty: pressing Enter makes a getty re-print its login prompt (or an
	// already-open shell re-print its prompt), so we see a prompt even though the
	// original one scrolled past before we attached.
	if err := s.send("\n"); err != nil {
		return err
	}

	fail := func(msg string) error {
		return fmt.Errorf("%s\n--- console output ---\n%s", msg, tail(raw.String(), 1500))
	}

	for {
		chunk, err := s.recv()
		if len(chunk) > 0 {
			raw.Write(chunk)
			win.WriteString(stripANSI(string(chunk)))

			// Answer cursor-position probes ONLY before a login/password prompt has
			// appeared. Probes matter while agetty draws the issue (it won't print
			// "login:" until its size probe is answered); but once a prompt is up the
			// tty echoes whatever we type, so a CPR reply sent then lands as literal
			// input in the login/password field (observed live: "^[[24;80R" typed
			// into the username → "Login incorrect"). The scan prepends the tail of
			// the previous chunk so a probe split across two websocket frames is still
			// seen; the carry is shorter than a full probe, so a probe counted in one
			// scan can never be re-counted by the next.
			if !promptSeen {
				scan := probeCarry + string(chunk)
				for range strings.Count(scan, cursorProbe) {
					if err := s.send(cursorReport); err != nil {
						return err
					}
				}
				if keep := len(cursorProbe) - 1; len(scan) > keep {
					probeCarry = scan[len(scan)-keep:]
				} else {
					probeCarry = scan
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return fail("console closed before the command completed")
			}
			return fail(fmt.Sprintf("console read error: %v", err))
		}
		text := win.String()

		// Once any login/password prompt is on screen, agetty is done probing and is
		// waiting for input — stop answering probes (see the probe block above).
		if endsWith(text, "login:") || endsWith(text, "password:") {
			promptSeen = true
		}

		switch phase {
		case phaseLogin:
			// The console may already sit at a shell (a previous session left it
			// logged in) — skip straight to running the command.
			if hasShellPrompt(text) {
				if err := s.send(command + "\n"); err != nil {
					return err
				}
				phase, win = phaseMarker, reset(win)
				continue
			}
			if endsWith(text, "login:") {
				// Clear any input the tty may already hold (e.g. an echoed CPR reply)
				// with Ctrl-U before typing the username, so the login field is clean.
				if err := s.send(killLine + login.User + "\n"); err != nil {
					return err
				}
				phase, win = phasePassword, reset(win)
			}
		case phasePassword:
			if endsWith(text, "password:") {
				if err := s.send(killLine + login.Password + "\n"); err != nil {
					return err
				}
				phase, win = phaseShell, reset(win)
			}
		case phaseShell:
			if strings.Contains(strings.ToLower(text), "login incorrect") {
				// The first "Login incorrect" can be a false negative — a CPR reply
				// echoed into the username field before we stopped answering probes.
				// getty immediately re-prompts, so retry the whole login sequence once
				// before giving up.
				if !retried {
					retried = true
					phase, win = phaseLogin, reset(win)
					continue
				}
				return fail("console login failed: incorrect password for " + login.User)
			}
			if hasShellPrompt(text) {
				if err := s.send(command + "\n"); err != nil {
					return err
				}
				phase, win = phaseMarker, reset(win)
			}
		case phaseMarker:
			if strings.Contains(text, marker) {
				_ = s.send("exit\n") // best-effort logout; failure is irrelevant now
				return nil
			}
		}
	}
}

// reset clears a builder and returns it, for the `win = reset(win)` idiom used to
// start matching fresh output after a phase change.
func reset(b strings.Builder) strings.Builder {
	b.Reset()
	return b
}

// endsWith reports whether text ends with suffix, case-insensitively and ignoring
// trailing whitespace — the shape of a tty prompt ("debian login: ", "Password:").
func endsWith(text, suffix string) bool {
	t := strings.ToLower(strings.TrimRight(text, " \t\r\n"))
	return strings.HasSuffix(t, suffix)
}

// hasShellPrompt reports whether the last non-empty line looks like a root shell
// prompt (ends with '#'), e.g. "root@ct:~#". Checking only the last line avoids
// matching a '#' that appears mid-MOTD.
func hasShellPrompt(text string) bool {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], " \t\r")
		if line == "" {
			continue
		}
		return strings.HasSuffix(line, "#")
	}
	return false
}

// newSentinel returns a random success marker and the shell fragment to echo it.
// The typed fragment embeds an empty-string concatenation ("") so the marker's
// literal text never appears in the command hlab types — only in its output — so
// matching the marker can't be fooled by the tty echoing the command back.
func newSentinel() (marker, sentinel string) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	n := hex.EncodeToString(b)
	return "HLAB_OK_" + n, `HLAB_OK""_` + n
}

// stripANSI removes ANSI escape sequences (CSI and OSC) and carriage returns from
// s so prompt/sentinel matching sees plain text regardless of terminal styling.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI: ESC [ ... <final 0x40..0x7e>
				i++
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			case ']': // OSC: ESC ] ... (BEL | ESC \)
				i++
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default: // a two-byte escape; drop the following byte
				i++
			}
			continue
		}
		if c == '\r' {
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// tail returns the last n bytes of s (whole string when shorter), prefixed with
// an ellipsis when truncated — used to attach recent console output to an error.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
