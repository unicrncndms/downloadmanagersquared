// dms-host is the native-messaging shim Firefox starts. It relays messages
// between the extension (length-prefixed JSON on stdio) and the engine (JSON
// lines on the named pipe), starting the engine if it isn't running.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows"

	"downloadmanagersquared/engine/internal/ipc"
	"downloadmanagersquared/engine/internal/winutil"
)

func main() {
	// Started by hand (stdin is a console, not Firefox's pipe): explain instead of relaying.
	var mode uint32
	if windows.GetConsoleMode(windows.Handle(os.Stdin.Fd()), &mode) == nil {
		os.Stdout.WriteString("dms-host.exe is the helper Firefox starts to talk to the download engine.\n" +
			"There is no need to run it yourself. The engine is dms-engine.exe (it runs in the tray).\n")
		return
	}
	conn, err := connect()
	if err != nil {
		writeFrame(os.Stdout, mustJSON(map[string]any{"event": "engineUnavailable", "data": err.Error()}))
		return
	}
	defer conn.Close()
	// The engine opens pages in the browser that started this host.
	browser, err := winutil.ParentExe()
	if err != nil {
		browser = ""
	}

	// Both relays write to Firefox: one write per frame, one writer at a time.
	out := &lockedWriter{w: os.Stdout}
	done := make(chan struct{}, 2)
	go func() { relayIn(os.Stdin, conn, out, browser); done <- struct{}{} }()
	go func() { relayOut(conn, out); done <- struct{}{} }()
	<-done
}

// maxToFirefox is the largest message Firefox takes from a native app; a
// longer one makes it drop the connection.
const maxToFirefox = 1 << 20

// relayIn passes Firefox's messages to the engine as JSON lines, until either
// side closes. Messages that aren't JSON are skipped; one longer than the
// engine reads is answered to Firefox (reply) with an error, if its id shows.
func relayIn(from io.Reader, to io.Writer, reply io.Writer, browser string) {
	for {
		msg, err := readFrame(from, ipc.MaxMessage-1) // -1: the newline
		if errors.Is(err, errTooLong) {
			if id := ipc.LineID(msg); id != nil {
				writeFrame(reply, ipc.TooLongReply(id))
			}
			continue
		}
		if err != nil {
			return
		}
		var buf bytes.Buffer
		if json.Compact(&buf, withBrowser(msg, browser)) != nil {
			continue
		}
		if m := method(msg); m == "pickFolder" || m == "pickSaveFolder" || m == "pickDir" || m == "browseFolder" {
			// The engine runs in the background, so Windows would put its
			// folder picker behind Firefox. Firefox started this host and is
			// in front, so the host may let the engine come forward.
			allowForeground()
		}
		buf.WriteByte('\n')
		if _, err := to.Write(buf.Bytes()); err != nil {
			return
		}
	}
}

// maxLine is the longest engine line relayed at all, whole or in chunks; a
// variable so the test needn't build one that long.
var maxLine = 64 << 20

// chunkSize is the slice of a long line one chunk message carries. A JSON
// string at most doubles it (quotes, backslashes), so a message stays well
// under maxToFirefox.
const chunkSize = 256 << 10

// chunks splits a line that Firefox would not take into messages
// {"chunk":i,"of":n,"id":id,"data":"..."} that the extension joins again
// (extension/lib/chunks.js). Cuts fall on rune boundaries, so each slice is
// valid in a JSON string.
func chunks(line []byte, id int) [][]byte {
	var parts [][]byte
	for len(line) > 0 {
		n := min(chunkSize, len(line))
		for n < len(line) && !utf8.RuneStart(line[n]) {
			n--
		}
		parts = append(parts, line[:n])
		line = line[n:]
	}
	out := make([][]byte, len(parts))
	for i, p := range parts {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.Encode(map[string]any{"chunk": i, "of": len(parts), "id": id, "data": string(p)})
		out[i] = bytes.TrimSpace(b.Bytes())
	}
	return out
}

// relayOut passes the engine's lines to Firefox as messages, until either side
// closes. A line over maxToFirefox goes in chunks (the lines that fit are sent
// as before, so an older extension loses nothing). One over maxLine is skipped
// rather than costing the connection, and Firefox is told so: a state too long
// would otherwise leave the pages showing an old list with nothing to say why.
func relayOut(from io.Reader, to io.Writer) {
	r := bufio.NewReaderSize(from, 64<<10)
	sent := 0 // lines chunked so far: each one's id
	for {
		line, err := ipc.ReadLine(r, maxLine)
		if err == nil && len(bytes.TrimSpace(line)) > maxToFirefox {
			sent++
			for _, m := range chunks(bytes.TrimSpace(line), sent) {
				if writeFrame(to, m) != nil {
					return
				}
			}
			continue
		}
		if errors.Is(err, ipc.ErrTooLong) {
			if writeFrame(to, []byte(`{"event":"tooLong"}`)) != nil {
				return
			}
			continue
		}
		if err != nil {
			return
		}
		if err := writeFrame(to, bytes.TrimSpace(line)); err != nil {
			return
		}
	}
}

// allowForeground lets the engine bring its next window to the front; tests
// replace it.
var allowForeground = winutil.AllowForeground

// method is the request's method, "" if it has none.
func method(msg []byte) string {
	var req struct {
		Method string `json:"method"`
	}
	json.Unmarshal(msg, &req)
	return req.Method
}

// withBrowser adds the browser's exe to the extension's hello. The exe is only
// ever the host's own finding: with none, any the extension sent is dropped,
// since the engine starts the browser that exe names.
func withBrowser(msg []byte, exe string) []byte {
	var req struct {
		ID     json.RawMessage            `json:"id"`
		Method string                     `json:"method"`
		Params map[string]json.RawMessage `json:"params"`
	}
	if json.Unmarshal(msg, &req) != nil || req.Method != "hello" {
		return msg
	}
	if exe == "" {
		if _, sent := req.Params["browser"]; !sent {
			return msg
		}
		delete(req.Params, "browser")
	} else {
		if req.Params == nil {
			req.Params = map[string]json.RawMessage{}
		}
		req.Params["browser"], _ = json.Marshal(exe)
	}
	b, err := json.Marshal(req)
	if err != nil {
		return msg
	}
	return b
}

func connect() (net.Conn, error) {
	if c, err := ipc.Dial(time.Second); err == nil {
		return c, nil
	}
	if err := startEngine(); err != nil {
		return nil, err
	}
	var lastErr error
	// 50 ms steps, 200 tries: the same 10 s budget as the old 250 ms x 40.
	for range 200 {
		time.Sleep(50 * time.Millisecond)
		c, err := ipc.Dial(time.Second)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// startEngine launches dms-engine.exe from this directory, breaking away from
// the browser's job object so the engine survives the browser closing.
func startEngine() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return winutil.StartDetached(filepath.Join(filepath.Dir(self), "dms-engine.exe"))
}

var errTooLong = errors.New("message too long")

// readFrame reads one length-prefixed message of at most max bytes. A longer
// one is read past, so the next can follow, and gives errTooLong with its
// first bytes.
func readFrame(r io.Reader, max int) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if int64(n) > int64(max) {
		head := make([]byte, min(int64(n), 256))
		if _, err := io.ReadFull(r, head); err != nil {
			return nil, err
		}
		if _, err := io.CopyN(io.Discard, r, int64(n)-int64(len(head))); err != nil {
			return nil, err
		}
		return head, errTooLong
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func writeFrame(w io.Writer, msg []byte) error {
	if len(msg) == 0 {
		return nil
	}
	b := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(msg)), uint32(len(msg)))
	_, err := w.Write(append(b, msg...))
	return err
}

// lockedWriter makes each Write whole when two goroutines share the writer.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
