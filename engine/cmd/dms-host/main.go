// dms-host is the native-messaging shim Firefox starts. It relays messages
// between the extension (length-prefixed JSON on stdio) and the engine (JSON
// lines on the named pipe), starting the engine if it isn't running.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

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

	done := make(chan struct{}, 2)
	go func() { // Firefox -> engine
		for {
			msg, err := readFrame(os.Stdin)
			if err != nil {
				break
			}
			var buf bytes.Buffer
			if json.Compact(&buf, withBrowser(msg, browser)) != nil {
				continue
			}
			buf.WriteByte('\n')
			if _, err := conn.Write(buf.Bytes()); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	go func() { // engine -> Firefox
		r := bufio.NewReaderSize(conn, 64<<10)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				break
			}
			if err := writeFrame(os.Stdout, bytes.TrimSpace(line)); err != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
}

// withBrowser adds the browser's exe to the extension's hello.
func withBrowser(msg []byte, exe string) []byte {
	if exe == "" {
		return msg
	}
	var req struct {
		ID     json.RawMessage            `json:"id"`
		Method string                     `json:"method"`
		Params map[string]json.RawMessage `json:"params"`
	}
	if json.Unmarshal(msg, &req) != nil || req.Method != "hello" {
		return msg
	}
	if req.Params == nil {
		req.Params = map[string]json.RawMessage{}
	}
	req.Params["browser"], _ = json.Marshal(exe)
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
	for range 40 {
		time.Sleep(250 * time.Millisecond)
		c, err := ipc.Dial(time.Second)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// startEngine launches dms-engine.exe from this directory, breaking away from
// Firefox's job object so the engine survives Firefox closing.
func startEngine() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	exe := filepath.Join(filepath.Dir(self), "dms-engine.exe")
	for _, flags := range []uint32{windows.CREATE_BREAKAWAY_FROM_JOB | windows.DETACHED_PROCESS, windows.DETACHED_PROCESS} {
		cmd := exec.Command(exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err = cmd.Start(); err == nil {
			cmd.Process.Release()
			return nil
		}
	}
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func writeFrame(w io.Writer, msg []byte) error {
	if len(msg) == 0 {
		return nil
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(msg)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(msg)
	return err
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
