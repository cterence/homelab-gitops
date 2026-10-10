package main

// End-to-end integration test: the whole binary, no harness inside the
// app. The test binary doubles as the catbox CLI (the helper-process
// trick: a CATBOX_HELPER=1 re-exec of os.Args[0] runs the real command
// and exits), so this file needs no bash glue and no in-process wiring:
// every step is the real CLI driving a real stash process, with real
// pairing over the real DERP network.
//
// The protocol is request-reply: the stash writes the roster, spool
// entry, or deletion before it replies, and a listener verifies and
// writes a file before its opDone — so a command's successful return
// is the synchronization point, asserted directly with no polling. The
// one async event (listener registration) is signaled by the process
// itself: its "listening" log line, read live off stderr on a channel.
//
// Gated because it needs outbound access to the DERP relays:
//
//	CATBOX_INTEGRATION=1 go test . -run TestIntegration -v -count=1

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain is the helper-process gate (see the file comment) and keeps
// t.TempDir() paths short: peer config dirs and lock files live under
// HOME, and long TMPDIR paths push macOS path limits.
func TestMain(m *testing.M) {
	if os.Getenv("CATBOX_HELPER") == "1" {
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, "catbox:", err)
			os.Exit(1)
		}

		os.Exit(0)
	}

	if root, err := os.MkdirTemp("/tmp", "catbox-test-"); err == nil {
		_ = os.Setenv("TMPDIR", root)
		code := m.Run()
		_ = os.RemoveAll(root)

		os.Exit(code)
	}

	os.Exit(m.Run())
}

// node is one real catbox: a HOME of its own plus a stash process
// running from it, with the stash's logs kept for failure reports.
type node struct {
	t      *testing.T
	name   string
	dir    string
	inbox  string
	logs   bytes.Buffer
	mu     sync.Mutex
	cmd    *exec.Cmd
	health string
}

// newNode makes a node (its own HOME) without starting anything.
func newNode(t *testing.T, name string) *node {
	t.Helper()
	dir := t.TempDir()

	return &node{
		t:     t,
		name:  name,
		dir:   dir,
		inbox: filepath.Join(dir, "inbox"),
	}
}

// env is the node's environment: its own HOME isolates the peer
// identity (peerDir is under os.UserConfigDir).
func (n *node) env(extra map[string]string) []string {
	out := []string{"HOME=" + n.dir, "CATBOX_HELPER=1", "CATBOX_FAST_TIMERS=1"}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}

	return out
}

// start runs the node's stash as a real subprocess and returns once
// its health endpoint answers. It returns errors instead of failing
// the test so the test goroutine stays in charge.
func (n *node) start() error {
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("node %s: reserving health port: %w", n.name, err)
	}

	n.health = port
	cmd := exec.Command(os.Args[0], "serve", "--data", n.dir, "--health", port)
	cmd.Env = n.env(map[string]string{"CATBOX_DATA": n.dir})
	n.mu.Lock()
	n.cmd = cmd
	cmd.Stdout = &n.logs
	cmd.Stderr = &n.logs
	n.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("node %s: starting stash: %w", n.name, err)
	}

	n.t.Cleanup(func() { n.stop() })

	return n.waitHealthy()
}

// stop terminates the node's subprocess, waiting for a graceful exit:
// the stash shuts down, the listener deregisters its address. Once
// the test has failed, the process gets a SIGQUIT first: the Go
// runtime dumps every goroutine's stack, which makes a wedged process
// post-mortem readable straight from the test output.
func (n *node) stop() {
	n.mu.Lock()
	cmd := n.cmd
	n.cmd = nil
	n.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	done := make(chan struct{})

	go func() { _ = cmd.Wait(); close(done) }()

	if n.t.Failed() {
		_ = cmd.Process.Signal(syscall.SIGQUIT)

		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()

		<-done
	}

	if n.t.Failed() {
		n.mu.Lock()
		logs := n.logs.String()
		n.mu.Unlock()
		n.t.Logf("node %s stash logs:\n%s", n.name, tail(logs, 60000))
	}
}

// joinMesh joins n to the stash at addr. The mesh's first node
// bootstraps as admin and joins by address; every later node presents
// the invite token minted by admin.
func joinMesh(t *testing.T, n, admin *node, addr string) {
	t.Helper()

	token := addr
	if n != admin {
		token = strings.TrimSpace(admin.cat(nil, "invite"))
	}

	out := n.cat(nil, "join", n.name, token)
	if !strings.Contains(out, "joined as "+n.name) {
		t.Fatalf("node %s: join output: %q", n.name, out)
	}
}

// cat runs one real CLI command as this node and returns its stdout.
func (n *node) cat(extra map[string]string, args ...string) string {
	n.t.Helper()
	return cat(n.t, n.env(extra), args...)
}

// addr prints the stash's tailcat address: a bearer capability, only
// ever handled inside the test.
func (n *node) addr() string {
	n.t.Helper()
	return strings.TrimSpace(n.cat(map[string]string{"CATBOX_DATA": n.dir}, "addr"))
}

// listen runs `recv --listen` as a long-lived subprocess and returns a
// channel that closes when the process itself reports registered: its
// "listening" log line, read live off stderr. The scanner keeps
// draining so the child never blocks on a full pipe.
func (n *node) listen() <-chan struct{} {
	n.t.Helper()

	ready := make(chan struct{})
	cmd := exec.Command(os.Args[0], "recv", "--dir", n.inbox, "--listen")
	cmd.Env = n.env(nil)

	pr, pw := io.Pipe()
	cmd.Stdout = io.MultiWriter(&n.logs, pw)

	cmd.Stderr = io.MultiWriter(&n.logs, pw)
	if err := cmd.Start(); err != nil {
		_ = pr.Close()

		n.t.Fatalf("node %s: starting listener: %v", n.name, err)
	}

	n.mu.Lock()
	n.cmd = cmd
	n.mu.Unlock()
	n.t.Cleanup(func() { n.stop() })

	go func() {
		registered := false
		sc := bufio.NewScanner(pr)

		for sc.Scan() {
			if !registered && strings.Contains(sc.Text(), `"msg":"listening"`) {
				registered = true

				close(ready)
			}
		}
	}()

	return ready
}

// TestIntegrationEndToEnd is the full user story against a real
// stash process and the real DERP network: two peers join with the
// stash's printed address, two files are sent directly to a listening
// peer in one command, then a third file rides the spool while the
// target is offline, and the target pulls it with recv. Asserts the
// files' contents, never internal state, and never sleeps: the CLI's
// return is the sync point.
func TestIntegrationEndToEnd(t *testing.T) {
	if os.Getenv("CATBOX_INTEGRATION") != "1" {
		t.Skip("set CATBOX_INTEGRATION=1 (needs outbound DERP network)")
	}

	t.Parallel()

	stash := newNode(t, "stash")
	if err := stash.start(); err != nil {
		t.Fatalf("starting stash: %v", err)
	}

	addr := stash.addr()

	milo := newNode(t, "milo")

	puma := newNode(t, "puma")
	for _, n := range []*node{milo, puma} {
		joinMesh(t, n, milo, addr)
	}

	// The stash writes the roster before replying to a join, so the
	// joins returning is the sync: both sides must see both members.
	if st := milo.cat(nil, "status"); !strings.Contains(st, "puma") {
		t.Fatalf("milo status does not show puma: %q", st)
	}

	if st := puma.cat(nil, "status"); !strings.Contains(st, "milo") {
		t.Fatalf("puma status does not show milo: %q", st)
	}

	// A direct send lands in the target's inbox with its content
	// intact, while the target's listener holds the door open. The
	// listener signals registration itself; two files ride one command.
	ready := puma.listen()

	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		t.Fatalf("puma listener never reported itself registered")
	}

	src := writeFile(t, "nap.txt", 128*1024)
	src2 := writeFile(t, "nap2.txt", 128*1024)

	out := milo.cat(nil, "send", "puma", src, src2)
	for _, name := range []string{"nap.txt", "nap2.txt"} {
		if !strings.Contains(out, "sent "+name+" to puma directly") {
			t.Fatalf("direct send output: %q", out)
		}
	}

	// "sent ... directly" only prints after the listener verified and
	// wrote the file: assert the bytes, don't wait for them.
	assertFile(t, puma, "nap.txt", src)
	assertFile(t, puma, "nap2.txt", src2)

	// Now the target goes offline — SIGTERM, so the listener
	// deregisters — and a third file must ride the spool. The spool
	// entry exists before the sender's command returns.
	puma.stop()

	src3 := writeFile(t, "nap3.txt", 128*1024)

	out = milo.cat(nil, "send", "puma", src3)
	if !strings.Contains(out, "sent nap3.txt to puma via stash") {
		t.Fatalf("spool send output: %q", out)
	}

	assertSpoolLen(t, stash, 1)

	// Back online, an explicit recv pulls the held file and acks it;
	// the stash deletes the spool entry before the ack reply.
	out = puma.cat(nil, "recv", "--dir", puma.inbox)
	if !strings.Contains(out, "got nap3.txt from milo") {
		t.Fatalf("pull output: %q", out)
	}

	assertFile(t, puma, "nap3.txt", src3)
	assertSpoolLen(t, stash, 0)
}

// TestIntegrationResume pins the resume story: a pull is killed
// mid-transfer, its partial survives, and the next pull continues at
// its chunk boundary instead of starting over — the phone-sleep case.
func TestIntegrationResume(t *testing.T) {
	if os.Getenv("CATBOX_INTEGRATION") != "1" {
		t.Skip("set CATBOX_INTEGRATION=1 (needs outbound DERP network)")
	}

	t.Parallel()

	stash := newNode(t, "stash")
	if err := stash.start(); err != nil {
		t.Fatalf("starting stash: %v", err)
	}

	addr := stash.addr()

	milo := newNode(t, "milo")

	puma := newNode(t, "puma")
	for _, n := range []*node{milo, puma} {
		joinMesh(t, n, milo, addr)
	}

	// Puma never listens: the file rides the spool. The payload must
	// be big enough that the killed pull is still running when its
	// partial appears — the kill window is the transfer itself.
	big := writeFile(t, "big.bin", 8<<20)

	out := milo.cat(nil, "send", "puma", big)
	if !strings.Contains(out, "sent big.bin to puma via stash") {
		t.Fatalf("spool send output: %q", out)
	}

	// A pull starts, then dies mid-transfer: SIGKILL leaves the
	// partial behind, torn tail and all.
	pull := exec.Command(os.Args[0], "recv", "--dir", puma.inbox)
	pull.Env = puma.env(nil)

	var pullLogs bytes.Buffer

	pull.Stdout = &pullLogs

	pull.Stderr = &pullLogs
	if err := pull.Start(); err != nil {
		t.Fatalf("starting pull: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		des, err := os.ReadDir(puma.inbox)
		if err == nil {
			for _, de := range des {
				if strings.HasPrefix(de.Name(), ".part-") {
					if fi, err := de.Info(); err == nil && fi.Size() >= 2*chunkSize {
						goto partial
					}
				}
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("pull never produced a partial:\n%s", pullLogs.String())

partial:
	_ = pull.Process.Kill()

	_ = pull.Wait()

	// The next pull resumes: the stash serves from the partial's
	// chunk boundary and the receiver finishes the file.
	out = puma.cat(nil, "recv", "--dir", puma.inbox)
	if !strings.Contains(out, "resumed from") {
		t.Fatalf("resumed pull output has no resume marker: %q", out)
	}

	assertFile(t, puma, "big.bin", big)
	assertSpoolLen(t, stash, 0)
}

// TestIntegrationDirectResume pins the direct-path resume: a listener
// dies mid-direct-send, the re-registration lets the next send
// continue at the receiver's partial — not from zero, and not from a
// double-skipped offset.
func TestIntegrationDirectResume(t *testing.T) {
	if os.Getenv("CATBOX_INTEGRATION") != "1" {
		t.Skip("set CATBOX_INTEGRATION=1 (needs outbound DERP network)")
	}

	t.Parallel()

	stash := newNode(t, "stash")
	if err := stash.start(); err != nil {
		t.Fatalf("starting stash: %v", err)
	}

	addr := stash.addr()

	milo := newNode(t, "milo")

	puma := newNode(t, "puma")
	for _, n := range []*node{milo, puma} {
		joinMesh(t, n, milo, addr)
	}

	// The payload must outlast the kill poll: the window is the
	// transfer itself.
	big := writeFile(t, "big.bin", 8<<20)

	// First send: direct, killed mid-transfer once the partial exists.
	ready := puma.listen()
	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		t.Fatalf("puma listener never reported itself registered")
	}

	send := exec.Command(os.Args[0], "send", "puma", big)
	send.Env = milo.env(nil)

	var sendLogs bytes.Buffer

	send.Stdout = &sendLogs

	send.Stderr = &sendLogs
	if err := send.Start(); err != nil {
		t.Fatalf("starting send: %v", err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		des, err := os.ReadDir(puma.inbox)
		if err == nil {
			for _, de := range des {
				if strings.HasPrefix(de.Name(), ".part-") {
					if fi, err := de.Info(); err == nil && fi.Size() >= 2*chunkSize {
						goto partial
					}
				}
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("direct send never produced a partial:\n%s", sendLogs.String())

partial:
	// The listener shuts down mid-flight (SIGTERM, like a phone
	// lock): the closed server kills the transfer now, the sender
	// falls back fast, and the partial is kept.
	puma.stop()

	_ = send.Wait()

	// The sender fell back to the stash: the file is parked, the
	// partial is kept.
	assertSpoolLen(t, stash, 1)

	// Second send: the listener is back, and the direct path must
	// continue from the partial.
	ready = puma.listen()
	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		t.Fatalf("puma listener never reported itself registered after restart")
	}

	out := milo.cat(nil, "send", "puma", big)
	if !strings.Contains(out, "resumed from") {
		t.Fatalf("resumed direct send output has no resume marker: %q", out)
	}

	assertFile(t, puma, "big.bin", big)

	// The first send's fallback copy stays parked at the stash: a
	// direct delivery does not ack spool items.
	assertSpoolLen(t, stash, 1)
}

// TestIntegrationInterruptedDeposit pins the locked-phone story: a
// listener vanishes without deregistering (SIGKILL, like a locked
// phone), the sender falls back to the stash, and SIGINT during the
// fallback deposit must abort the command instead of riding it out —
// then a re-send resumes at the stash's chunk boundary, not from zero.
func TestIntegrationInterruptedDeposit(t *testing.T) {
	if os.Getenv("CATBOX_INTEGRATION") != "1" {
		t.Skip("set CATBOX_INTEGRATION=1 (needs outbound DERP network)")
	}

	t.Parallel()

	stash := newNode(t, "stash")
	if err := stash.start(); err != nil {
		t.Fatalf("starting stash: %v", err)
	}

	addr := stash.addr()

	milo := newNode(t, "milo")

	puma := newNode(t, "puma")
	for _, n := range []*node{milo, puma} {
		joinMesh(t, n, milo, addr)
	}

	// The payload must outlast the kill poll: the SIGINT window is
	// the deposit itself.
	big := writeFile(t, "big.bin", 8<<20)

	// Puma's listener is up, then killed without deregistering: the
	// roster keeps a stale address, exactly like a locked phone.
	ready := puma.listen()
	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		t.Fatalf("puma listener never reported itself registered")
	}

	puma.mu.Lock()
	listener := puma.cmd
	puma.cmd = nil
	puma.mu.Unlock()

	if listener == nil || listener.Process == nil {
		t.Fatalf("node puma: no listener process to kill")
	}

	if err := listener.Process.Kill(); err != nil {
		t.Fatalf("killing puma listener: %v", err)
	}

	_, _ = listener.Process.Wait()

	// The send: direct dial fails on the stale address, the deposit
	// at the stash starts, and SIGINT must land mid-deposit.
	send := exec.Command(os.Args[0], "send", "puma", big)
	send.Env = milo.env(nil)

	var sendLogs bytes.Buffer

	send.Stdout = &sendLogs

	send.Stderr = &sendLogs
	if err := send.Start(); err != nil {
		t.Fatalf("starting send: %v", err)
	}

	spoolDir := filepath.Join(stash.dir, "spool")

	// The dead listener is retried before the fallback: the deposit
	// starts after the retry window, so the poll waits it out.
	deadline := time.Now().Add(directRetryWindow + 90*time.Second)
	for time.Now().Before(deadline) {
		if des, err := os.ReadDir(spoolDir); err == nil {
			for _, de := range des {
				name := de.Name()
				if !strings.HasPrefix(name, ".put-") && !strings.HasPrefix(name, ".part-") {
					continue
				}

				// One whole chunk at least: a resume marker needs it.
				if fi, err := de.Info(); err == nil && fi.Size() >= headerLen+chunkFrameLen {
					goto depositing
				}
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	_ = send.Process.Kill()

	t.Fatalf("send never started depositing:\n%s", sendLogs.String())

depositing:
	_ = send.Process.Signal(os.Interrupt)

	done := make(chan error, 1)
	go func() { done <- send.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("SIGINT did not abort the send:\n%s", sendLogs.String())
		}
	case <-time.After(30 * time.Second):
		_ = send.Process.Kill()

		<-done

		t.Fatalf("SIGINT did not kill the send within 30s:\n%s", sendLogs.String())
	}

	if out := sendLogs.String(); strings.Contains(out, "sent big.bin") {
		t.Fatalf("aborted send still reported success:\n%s", out)
	}

	// No spool entry: an aborted deposit stays a partial.
	assertSpoolLen(t, stash, 0)

	// Re-send: the stash holds the partial, so the deposit resumes at
	// its chunk boundary instead of starting over.
	out := milo.cat(nil, "send", "puma", big)
	if !strings.Contains(out, "resumed from") {
		t.Fatalf("re-send did not resume the interrupted deposit: %q", out)
	}

	assertSpoolLen(t, stash, 1)

	// The parked file pulls clean: both attempts' frames line up.
	out = puma.cat(nil, "recv", "--dir", puma.inbox)
	if !strings.Contains(out, "got big.bin from milo") {
		t.Fatalf("pull output: %q", out)
	}

	assertFile(t, puma, "big.bin", big)
	assertSpoolLen(t, stash, 0)
}

// TestIntegrationStashlessDirect: with the stash dead, a send to a
// listening peer rides the cached roster alone — the stash is a
// convenience for sends, not a dependency (catbox #902 P1).
func TestIntegrationStashlessDirect(t *testing.T) {
	if os.Getenv("CATBOX_INTEGRATION") != "1" {
		t.Skip("set CATBOX_INTEGRATION=1 (needs outbound DERP network)")
	}

	t.Parallel()

	stash := newNode(t, "stash")
	if err := stash.start(); err != nil {
		t.Fatalf("starting stash: %v", err)
	}

	addr := stash.addr()

	milo := newNode(t, "milo")

	puma := newNode(t, "puma")
	for _, n := range []*node{milo, puma} {
		joinMesh(t, n, milo, addr)
	}

	// puma listens; milo's status caches its address.
	ready := puma.listen()

	select {
	case <-ready:
	case <-time.After(60 * time.Second):
		t.Fatalf("puma listener never reported itself registered")
	}

	if st := milo.cat(nil, "status"); !strings.Contains(st, "listening") {
		t.Fatalf("milo status does not show puma listening: %q", st)
	}

	stash.stop()

	src := writeFile(t, "derp.txt", 128*1024)

	out := milo.cat(nil, "send", "puma", src)
	if !strings.Contains(out, "sent derp.txt to puma directly") {
		t.Fatalf("stashless send output: %q", out)
	}

	assertFile(t, puma, "derp.txt", src)
}

// ---- helpers ----

// cat runs the catbox binary as a helper subprocess with the given
// environment and args, failing the test on a non-zero exit.
func cat(t *testing.T, env []string, args ...string) string {
	t.Helper()

	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = env

	var out, errBuf bytes.Buffer

	cmd.Stdout = &out

	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("catbox %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, out.String(), errBuf.String())
	}

	return out.String()
}

func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = ln.Close() }()

	return ln.Addr().String(), nil
}

func (n *node) waitHealthy() error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + n.health + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("node %s: stash never became healthy on %s", n.name, n.health)
}

// assertFile pins the node's inbox holding name with exactly the
// source file's content: a successful send/pull means it is there.
func assertFile(t *testing.T, n *node, name, srcPath string) {
	t.Helper()

	want, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("reading source: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(n.inbox, name))
	if err != nil {
		t.Fatalf("node %s reading %s: %v", n.name, name, err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("node %s received %s with different content (%d vs %d bytes)", n.name, name, len(got), len(want))
	}
}

// assertSpoolLen pins the stash's held-item count (one .json meta
// per item): deposits and post-ack deletions happen before the
// client's reply, so the count is settled by the return.
func assertSpoolLen(t *testing.T, n *node, want int) {
	t.Helper()

	des, err := os.ReadDir(filepath.Join(n.dir, "spool"))
	if err != nil {
		t.Fatalf("reading spool: %v", err)
	}

	count := 0

	for _, d := range des {
		if strings.HasSuffix(d.Name(), ".json") {
			count++
		}
	}

	if count != want {
		t.Fatalf("spool holds %d items, want %d", count, want)
	}
}

func writeFile(t *testing.T, name string, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)

	content := bytes.Repeat([]byte("catbox end-to-end test file\n"), size/27+1)
	if err := os.WriteFile(path, content[:size], 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	return path
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[len(s)-n:]
}
