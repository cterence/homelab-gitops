package main

// Peer operations: one-shot tailcat client connections. No listener,
// no resident process: dial, do one op, exit.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
)

// lockPeer serializes one-shot client commands: tailcat allows one
// tunnel per peer key per server, so a second concurrent command
// would hang at dial. Non-blocking — fail fast, retry when free. A
// stale lock (crashed process) is detected by pid and reclaimed.
func lockPeer() (release func(), err error) {
	dir := peerConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}, nil // no config dir: proceed unlocked
	}

	path := filepath.Join(dir, "client.lock")

	if b, rerr := os.ReadFile(path); rerr == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
			if kerr := syscall.Kill(pid, 0); kerr == nil || errors.Is(kerr, syscall.EPERM) {
				return nil, errors.New("another catbox command is already running for this identity")
			}
		}

		_ = os.Remove(path) // stale: its process is gone
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, errors.New("another catbox command is already running for this identity")
		}

		return func() {}, nil // cannot lock: proceed unlocked
	}

	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()

	return func() { _ = os.Remove(path) }, nil
}

// clientConn opens a tailcat tunnel to the storer and dials the
// protocol port. Close both when done.
func clientConn(ctx context.Context, id *peerID) (net.Conn, *tailcat.Client, error) {
	c := &tailcat.Client{Server: id.StorerAddr, Key: id.DialKey, Logf: func(string, ...any) {}}

	conn, err := c.DialTCPPort(ctx, catboxPort)
	if err != nil {
		_ = c.Close()

		return nil, nil, fmt.Errorf("connecting to storer: %w", err)
	}

	// A blocked write ignores the context: closing the conn is the
	// only thing that unblocks it, so Ctrl-C must close the conn.
	// These processes are one-shot: the watcher lives until they exit.
	go func() {
		<-ctx.Done()

		_ = conn.Close()
	}()

	return conn, c, nil
}

// joinReq joins the mesh (idempotent) and caches the roster. addr is
// this peer's listener address, empty when not listening.
func joinReq(rwc io.ReadWriter, id *peerID, addr tailcat.Addr) error {
	pub, err := id.Key.Public().MarshalText()
	if err != nil {
		return err
	}

	if err := writeMsg(rwc, msg{Op: opJoin, Name: id.Name, Key: string(pub), Addr: string(addr)}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opJoined || !m.OK {
		return fmt.Errorf("join rejected: %s", m.Err)
	}

	_ = saveRoster(rosterPath(peerConfigDir()), m.Members) // cache only

	return nil
}

// clientStatus prints the roster and this peer's pending items, straight
// from the storer, to out.
func clientStatus(rwc io.ReadWriter, out io.Writer, id *peerID) error {
	if err := writeMsg(rwc, msg{Op: opPending}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opItems || !m.OK {
		return fmt.Errorf("status: %s", m.Err)
	}

	_ = saveRoster(rosterPath(peerConfigDir()), m.Members) // cache only

	var waitBytes int64

	for _, it := range m.Items {
		waitBytes += it.Plain
	}

	_, _ = fmt.Fprintf(out, "inbox: %d waiting (%s)\n", len(m.Items), humanBytes(waitBytes))

	for _, it := range m.Items {
		_, _ = fmt.Fprintf(out, "  %s from %s (%s), id %s\n", it.FileName, it.From, humanBytes(it.Plain), it.ID)
	}

	_, _ = fmt.Fprintf(out, "members: %d\n", len(m.Members))

	slices.SortFunc(m.Members, func(a, b member) int {
		return strings.Compare(a.Name, b.Name)
	})

	for _, mem := range m.Members {
		marker := ""
		if mem.Name == id.Name {
			marker = " [you]"
		}

		if mem.Addr != "" {
			marker += " (listening)"
		}

		_, _ = fmt.Fprintf(out, "  %s%s\n", mem.Name, marker)
	}

	return nil
}

func peerConfigDir() string {
	dir, err := peerDir()
	if err != nil {
		return "catbox"
	}

	return dir
}

// refreshRoster asks the storer for the roster via a pending query.
func refreshRoster(rwc io.ReadWriter) ([]member, error) {
	if err := writeMsg(rwc, msg{Op: opPending}); err != nil {
		return nil, err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return nil, err
	}

	if m.Op != opItems || !m.OK {
		return nil, fmt.Errorf("roster refresh: %s", m.Err)
	}

	return m.Members, nil
}

// clientSend seals file to target: directly when the target is
// listening, else deposited at the storer. Direct is tried first with
// a short timeout; the storer is always the fallback. rwc is the
// already-open storer connection.
func clientSend(ctx context.Context, rwc io.ReadWriteCloser, id *peerID, targetName, path string) error {
	if targetName == id.Name {
		return errors.New("can't send to yourself")
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	// The cached roster may lag a listener that just came online; the
	// storer is the authority, and the tunnel is already open.
	roster, err := refreshRoster(rwc)
	if err != nil {
		return err
	}

	_ = saveRoster(rosterPath(peerConfigDir()), roster) // cache only

	target, ok := memberByName(roster, targetName)
	if !ok {
		return fmt.Errorf("no member named %q", targetName)
	}

	if target.Addr != "" {
		fmt.Fprintf(os.Stderr, "dialing %s directly...\n", targetName)

		err := directSend(ctx, id, target, f, info, path)
		if err == nil {
			fmt.Printf("sent %s to %s directly (%s)\n", filepath.Base(path), targetName, humanBytes(info.Size()))
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err() // Ctrl-C: the raw error is just the closed conn
		}
		// Target unreachable: fall through to the storer.
		fmt.Fprintf(os.Stderr, "direct send failed (%v), depositing at the storer\n", err)

		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}

	if err := writeMsg(rwc, msg{Op: opSend, Target: targetName, FileName: filepath.Base(path), Size: info.Size()}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opReady || !m.OK {
		return fmt.Errorf("storer refused: %s", m.Err)
	}

	if len(m.Members) > 0 {
		_ = saveRoster(rosterPath(peerConfigDir()), m.Members) // cache only
	}

	fmt.Fprintf(os.Stderr, "depositing %s at the storer...\n", humanBytes(info.Size()))

	src := &progressReader{r: f, total: info.Size(), label: "depositing", every: time.Second}

	plainSize, sha, err := sealStream(id.Key, target.Key, rwc, src)
	src.close() // a failed write never reaches EOF: the line must not linger

	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // Ctrl-C: the raw error is just the closed conn
		}

		return err
	}

	if plainSize != info.Size() {
		return fmt.Errorf("sealed %d bytes but file is %d", plainSize, info.Size())
	}

	if err := writeMsg(rwc, msg{Op: opSent, SHA: sha}); err != nil {
		return err
	}

	m, err = readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opDone || !m.OK {
		return fmt.Errorf("deposit failed: %s", m.Err)
	}

	fmt.Printf("sent %s to %s via storer (%s, sha256 %s)\n", filepath.Base(path), targetName, humanBytes(info.Size()), sha[:12])

	return nil
}

// directSend dials the target's listener and hands it the sealed file.
func directSend(ctx context.Context, id *peerID, target member, f *os.File, info os.FileInfo, path string) error {
	c := &tailcat.Client{Server: target.Addr, Key: id.DialKey, Logf: func(string, ...any) {}}

	defer func() { _ = c.Close() }()

	// A live listener answers its handshake in well under a second;
	// anything longer is a stale roster address — fail fast and let
	// the caller fall back to the storer.
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	conn, err := c.DialTCPPort(dialCtx, catboxPort)
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	// Like clientConn: Ctrl-C closes the conn to unblock a stuck
	// write — on the parent ctx, not the 3s dial timeout.
	go func() {
		<-ctx.Done()

		_ = conn.Close()
	}()

	_ = conn.SetDeadline(time.Now().Add(idleTimeout))

	if err := writeMsg(conn, msg{Op: opSend, Target: target.Name, FileName: filepath.Base(path), Size: info.Size()}); err != nil {
		return err
	}

	m, err := readMsg(conn)
	if err != nil {
		return err
	}

	if m.Op != opReady || !m.OK {
		return fmt.Errorf("target refused: %s", m.Err)
	}

	src := &progressReader{r: f, total: info.Size(), label: "sending", every: time.Second, onTick: func(int64) {
		_ = conn.SetDeadline(time.Now().Add(idleTimeout)) // sliding: inactivity cap, not total
	}}

	_, sha, err := sealStream(id.Key, target.Key, conn, src)
	src.close() // a failed write never reaches EOF: the line must not linger

	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // Ctrl-C: the raw error is just the closed conn
		}

		return err
	}

	if err := writeMsg(conn, msg{Op: opSent, SHA: sha}); err != nil {
		return err
	}

	m, err = readMsg(conn)
	if err != nil {
		return err
	}

	if m.Op != opDone || !m.OK {
		return fmt.Errorf("direct send failed: %s", m.Err)
	}

	return nil
}

// clientDismiss refuses delivery of one pending item: the storer
// deletes it without ever transferring the bytes.
func clientDismiss(rwc io.ReadWriter, id string) error {
	if err := writeMsg(rwc, msg{Op: opDismiss, ID: id}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opAcked || !m.OK {
		return fmt.Errorf("dismiss: %s", m.Err)
	}

	return nil
}

// clientInbox pulls everything held for this peer into dir.
func clientInbox(ctx context.Context, rwc io.ReadWriteCloser, id *peerID, dir string) error {
	if err := writeMsg(rwc, msg{Op: opPending}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opItems || !m.OK {
		return fmt.Errorf("pending: %s", m.Err)
	}

	if len(m.Members) > 0 {
		_ = saveRoster(rosterPath(peerConfigDir()), m.Members) // cache only
	}

	if len(m.Items) == 0 {
		fmt.Println("inbox empty")
		return nil
	}

	var pullBytes int64

	for _, it := range m.Items {
		pullBytes += it.Plain
	}

	fmt.Fprintf(os.Stderr, "pulling %d files (%s)...\n", len(m.Items), humanBytes(pullBytes))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, it := range m.Items {
		if err := clientFetch(rwc, id, it, dir); err != nil {
			return err
		}
	}

	return nil
}

// clientFetch pulls, verifies, and acknowledges one item.
func clientFetch(rwc io.ReadWriteCloser, id *peerID, it item, dir string) error {
	if err := writeMsg(rwc, msg{Op: opFetch, ID: it.ID}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opFile || !m.OK {
		return fmt.Errorf("fetch %s: %s", it.ID, m.Err)
	}

	dst := uniquePath(dir, m.FileName)

	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	src := &progressReader{r: io.LimitReader(rwc, m.Size), total: it.Plain, label: "received", every: time.Second}

	// The stream ends at its terminator; no size bound needed here.
	_, plainSize, sha, err := openStream(id.Key, src, tmp)
	src.close() // sealed streams self-terminate: no EOF reaches the reader

	_ = tmp.Close()

	if err != nil {
		return err
	}

	if m.SHA != "" && sha != m.SHA {
		return fmt.Errorf("%s: sha mismatch (got %s, want %s)", m.FileName, sha, m.SHA)
	}

	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}

	if err := writeMsg(rwc, msg{Op: opAck, ID: it.ID}); err != nil {
		return err
	}

	ack, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if ack.Op != opAcked || !ack.OK {
		return fmt.Errorf("ack %s: %s", it.ID, ack.Err)
	}

	fmt.Printf("got %s from %s (%s)\n", filepath.Base(dst), m.From, humanBytes(plainSize))

	return nil
}

// statusJSON is the machine-readable answer to status --json.
type statusJSON struct {
	Name    string   `json:"name"`
	Waiting []item   `json:"waiting"`
	Members []member `json:"members"`
}

// clientStatusJSON writes the storer's answer as one JSON object.
func clientStatusJSON(rwc io.ReadWriter, out io.Writer, id *peerID) error {
	if err := writeMsg(rwc, msg{Op: opPending}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opItems || !m.OK {
		return fmt.Errorf("status: %s", m.Err)
	}

	_ = saveRoster(rosterPath(peerConfigDir()), m.Members) // cache only

	slices.SortFunc(m.Members, func(a, b member) int {
		return strings.Compare(a.Name, b.Name)
	})

	// Never emit JSON null: the phone app reads these as arrays.
	waiting := m.Items

	if waiting == nil {
		waiting = []item{}
	}

	members := m.Members

	if members == nil {
		members = []member{}
	}

	enc := json.NewEncoder(out)

	return enc.Encode(statusJSON{Name: id.Name, Waiting: waiting, Members: members})
}

// uniquePath returns dir/name, or dir/base(N).ext when it already
// exists, so received files never overwrite each other.
func uniquePath(dir, name string) string {
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err != nil {
		return dst
	}

	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	for i := 1; ; i++ {
		dst = filepath.Join(dir, fmt.Sprintf("%s(%d)%s", base, i, ext))
		if _, err := os.Stat(dst); err != nil {
			return dst
		}
	}
}

// downloadDir is the default inbox: the OS downloads dir + /catbox.
func downloadDir() (string, error) {
	if xdg := os.Getenv("XDG_DOWNLOAD_DIR"); xdg != "" {
		return filepath.Join(xdg, "catbox"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, "Downloads", "catbox"), nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	v := float64(n)
	for _, s := range []string{"KiB", "MiB", "GiB", "TiB"} {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, s)
		}
	}

	return fmt.Sprintf("%.1f PiB", v)
}
