package main

// End-to-end integration test: the whole binary, no harness inside the
// app. The test binary doubles as the catbox CLI (the helper-process
// trick: a CATBOX_HELPER=1 re-exec of os.Args[0] runs the real command
// and exits), so this file needs no bash glue and no in-process wiring:
// every step is the real CLI driving a real storer process, with real
// pairing over the real DERP network.
//
// Gated because it needs outbound access to the DERP relays:
//
//	CATBOX_INTEGRATION=1 go test . -run TestIntegration -v -count=1

import (
	"bytes"
	"fmt"
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

// node is one real catbox: a HOME of its own plus a storer process
// running from it, with the storer's logs kept for failure reports.
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
	out := []string{"HOME=" + n.dir, "CATBOX_HELPER=1"}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}

	return out
}

// start runs the node's storer as a real subprocess and returns once
// its health endpoint answers, so the test never sleeps on startup.
// It returns errors instead of failing the test so the test goroutine
// stays in charge.
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
		return fmt.Errorf("node %s: starting storer: %w", n.name, err)
	}

	n.t.Cleanup(func() { n.stop() })

	return n.waitHealthy()
}

// stop terminates the storer process, waiting for a graceful exit.
// Once the test has failed, the process gets a SIGQUIT first: the Go
// runtime dumps every goroutine's stack, which makes a wedged storer
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
		n.t.Logf("node %s storer logs:\n%s", n.name, tail(logs, 60000))
	}
}

// cat runs one real CLI command as this node and returns its stdout.
func (n *node) cat(extra map[string]string, args ...string) string {
	n.t.Helper()
	return cat(n.t, n.env(extra), args...)
}

// addr prints the storer's tailcat address: a bearer capability, only
// ever handled inside the test.
func (n *node) addr() string {
	n.t.Helper()
	return strings.TrimSpace(n.cat(map[string]string{"CATBOX_DATA": n.dir}, "addr"))
}

// listen runs `recv --listen` as a long-lived subprocess: the node is
// directly reachable until stopListener.
func (n *node) listen() {
	n.t.Helper()
	cmd := exec.Command(os.Args[0], "recv", "--dir", n.inbox, "--listen")
	cmd.Env = n.env(nil)
	cmd.Stdout = &n.logs

	cmd.Stderr = &n.logs
	if err := cmd.Start(); err != nil {
		n.t.Fatalf("node %s: starting listener: %v", n.name, err)
	}

	n.cmd = cmd
	n.t.Cleanup(func() { n.stop() })
}

// TestIntegrationEndToEnd is the full user story against a real
// storer process and the real DERP network: two peers join with the
// storer's printed address, a file is sent directly to a listening
// peer, then a second file rides the spool while the target is
// offline, and the target pulls it with recv. Asserts the files'
// contents, not internal state.
func TestIntegrationEndToEnd(t *testing.T) {
	if os.Getenv("CATBOX_INTEGRATION") != "1" {
		t.Skip("set CATBOX_INTEGRATION=1 (needs outbound DERP network)")
	}

	storer := newNode(t, "storer")
	if err := storer.start(); err != nil {
		t.Fatalf("starting storer: %v", err)
	}

	addr := storer.addr()

	milo := newNode(t, "milo")

	puma := newNode(t, "puma")
	for _, n := range []*node{milo, puma} {
		out := n.cat(nil, "join", "--name", n.name, addr)
		if !strings.Contains(out, "joined as "+n.name) {
			t.Fatalf("node %s: join output: %q", n.name, out)
		}
	}

	waitFor(t, func() bool {
		return strings.Contains(milo.cat(nil, "status"), "puma") &&
			strings.Contains(puma.cat(nil, "status"), "milo")
	}, "the roster to show both members on both sides")

	// A direct send lands in the target's inbox with its content
	// intact, while the target's listener holds the door open.
	puma.listen()

	src := writeFile(t, "nap.txt", 128*1024)
	waitFor(t, func() bool {
		return strings.Contains(puma.cat(nil, "status"), "(listening)")
	}, "puma to publish its listener address")

	out := milo.cat(nil, "send", "puma", src)
	if !strings.Contains(out, "sent nap.txt to puma directly") {
		t.Fatalf("direct send output: %q", out)
	}

	waitForFile(t, puma, "nap.txt", src)

	// Now the target goes offline and a second file must ride the
	// spool: the direct dial fails and the storer holds it.
	puma.stop()

	src2 := writeFile(t, "nap2.txt", 128*1024)

	out = milo.cat(nil, "send", "puma", src2)
	if !strings.Contains(out, "sent nap2.txt to puma via storer") {
		t.Fatalf("spool send output: %q", out)
	}

	waitFor(t, func() bool {
		des, err := os.ReadDir(filepath.Join(storer.dir, "spool"))
		return err == nil && len(des) > 0
	}, "the storer to hold the offline peer's file")

	// Back online, an explicit recv pulls the held file and acks it.
	out = puma.cat(nil, "recv", "--dir", puma.inbox)
	if !strings.Contains(out, "got nap2.txt from milo") {
		t.Fatalf("pull output: %q", out)
	}

	waitForFile(t, puma, "nap2.txt", src2)
	waitFor(t, func() bool {
		des, err := os.ReadDir(filepath.Join(storer.dir, "spool"))
		return err == nil && len(des) == 0
	}, "the storer to drop the file after the ack")
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

	return fmt.Errorf("node %s: storer never became healthy on %s", n.name, n.health)
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(250 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

// waitForFile waits until the node's inbox holds name with exactly the
// source file's content.
func waitForFile(t *testing.T, n *node, name, srcPath string) {
	t.Helper()

	want, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("reading source: %v", err)
	}

	waitFor(t, func() bool {
		got, err := os.ReadFile(filepath.Join(n.inbox, name))
		return err == nil && bytes.Equal(got, want)
	}, fmt.Sprintf("%s to receive %s", n.name, name))
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
