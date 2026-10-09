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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

// lockPeer serializes one-shot client commands: tailcat allows one
// tunnel per peer key per server, so concurrent commands queue in
// client.queue and take the lock in arrival order (or give up with
// ctx). Both the lock and every queue slot is an flock — a crashed
// process releases them by dying, so a stale lock or a reused pid can
// never jam the queue.
func lockPeer(ctx context.Context) (release func(), err error) {
	dir := peerConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}, nil // no config dir: proceed unlocked
	}

	queue := filepath.Join(dir, "client.queue")
	if err := os.MkdirAll(queue, 0o700); err != nil {
		return func() {}, nil // cannot queue: proceed unlocked
	}

	// The queue slot: nanosecond names put waiters in arrival order,
	// and its flock is the waiter's liveness — death releases it.
	var (
		entry string
		slot  *os.File
	)

	for slot == nil {
		name := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())

		f, err := os.OpenFile(filepath.Join(queue, name), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if os.IsExist(err) {
			continue // same-nanosecond collision: take the next nanosecond
		}

		if err != nil {
			return func() {}, nil // cannot queue: proceed unlocked
		}

		if ferr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); ferr != nil {
			_ = f.Close()
			_ = os.Remove(filepath.Join(queue, name))

			return func() {}, nil // cannot queue: proceed unlocked
		}

		slot = f
		entry = name
	}

	if err := ctx.Err(); err != nil {
		_ = slot.Close()
		_ = os.Remove(filepath.Join(queue, entry))

		return nil, err // a canceled command never runs
	}

	giveUp := func() {
		_ = slot.Close()
		_ = os.Remove(filepath.Join(queue, entry))
	}

	lockPath := filepath.Join(dir, "client.lock")

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	var logged bool

	for {
		head, ahead := queueState(queue, entry)
		if head == entry {
			if lf, ok := takeLock(lockPath); ok {
				_ = slot.Close()
				_ = os.Remove(filepath.Join(queue, entry))

				return func() { _ = lf.Close() }, nil
			}
		}

		// Waiting must say so: a silent stall looks like a hang.
		if !logged {
			note := "another catbox command is running, queued"
			if ahead > 0 {
				note = fmt.Sprintf("%s (%d ahead)", note, ahead)
			}

			fmt.Fprintln(os.Stderr, note)

			logged = true
		}

		select {
		case <-ctx.Done():
			giveUp()

			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

// takeLock claims the client lock: an flock on client.lock, held by
// the returned file until it closes — the holder's death releases it.
func takeLock(path string) (*os.File, bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		return nil, false
	}

	return f, true
}

// queueState reports the first live queue entry and how many live
// ones sit ahead of ours. A slot's flock is its waiter's liveness:
// a probe that locks the slot means its waiter is gone — the slot is
// removed — so a crashed waiter never jams the queue.
func queueState(queue, ours string) (head string, ahead int) {
	des, err := os.ReadDir(queue)
	if err != nil {
		return ours, 0 // unreadable queue: fall through to the lock claim
	}

	names := make([]string, 0, len(des))
	for _, de := range des {
		names = append(names, de.Name())
	}

	slices.Sort(names)

	for _, name := range names {
		if name == ours {
			break // ours: everything live ahead of it is counted
		}

		f, err := os.OpenFile(filepath.Join(queue, name), os.O_RDWR, 0o600)
		if err != nil {
			continue
		}

		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close() // held: its waiter lives
			ahead++

			if head == "" {
				head = name
			}

			continue
		}

		_ = f.Close() // the probe took it: the waiter is dead
		_ = os.Remove(filepath.Join(queue, name))
	}

	if head == "" {
		head = ours // nothing live ahead: our turn
	}

	return head, ahead
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

// pathOf reads a peer's current network path out of a tailcat server
// status: the direct endpoint, or the DERP relay.
func pathOf(st *ipnstate.Status, peer key.NodePublic) string {
	if st == nil {
		return ""
	}

	if ps := st.Peer[peer]; ps != nil {
		if ps.CurAddr != "" {
			return "direct " + ps.CurAddr
		}

		if ps.Relay != "" {
			return "DERP relay " + ps.Relay
		}
	}

	return ""
}

// discoPath reports the network path a client's tunnel is using: one
// bounded disco ping. Empty when it cannot answer in time — the report
// must never fail the transfer.
func discoPath(ctx context.Context, c *tailcat.Client) string {
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	r, err := c.DiscoPing(pctx)
	if err != nil {
		return ""
	}

	if r.Endpoint != "" {
		return "direct " + r.Endpoint
	}

	if r.DERPRegionID != 0 {
		return fmt.Sprintf("DERP relay (region %d)", r.DERPRegionID)
	}

	return ""
}

// pathSuffix formats the path report for a transfer output line.
func pathSuffix(p string) string {
	if p == "" {
		return ", path: unknown" // the report must never fail the transfer, but must never hide either
	}

	return ", path: " + p
}

// resumedSuffix formats the resume offset for a transfer output line.
func resumedSuffix(offset int64) string {
	if offset <= 0 {
		return ""
	}

	return ", resumed from " + humanBytes(offset)
}

// storerConn dials a fresh storer connection on the shared engine.
// Ctrl-C must close the conn to unblock a stuck write.
func storerConn(ctx context.Context, id *peerID) (io.ReadWriteCloser, error) {
	conn, err := tunnelClient(id.StorerAddr, id.DialKey).DialTCPPort(ctx, catboxPort)
	if err != nil {
		return nil, fmt.Errorf("connecting to storer: %w", err)
	}

	go func() {
		<-ctx.Done()

		_ = conn.Close()
	}()

	return conn, nil
}

// resumeOffset validates a receiver-advertised resume point: only a
// chunk-aligned prefix of a smaller file can be trusted.
func resumeOffset(have, size int64) int64 {
	if have < 0 || have%chunkSize != 0 || have >= size {
		return 0
	}

	return have
}

// livePath polls the tunnel's network path in the background — one
// bounded disco ping every few seconds, off the data path — so
// progress lines can show it, including its relay-to-direct upgrade
// mid-transfer. stop ends the poller; the getter returns the last
// answer, "" before the first one lands.
func livePath(ctx context.Context, c *tailcat.Client) (get func() string, stop func()) {
	var (
		mu sync.Mutex
		p  string
	)

	pctx, cancel := context.WithCancel(ctx)

	go func() {
		defer cancel()

		poll := func() {
			if s := discoPath(pctx, c); s != "" {
				mu.Lock()
				p = s
				mu.Unlock()
			}
		}

		poll() // the first answer before the first tick

		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()

		for {
			select {
			case <-pctx.Done():
				return
			case <-tick.C:
				poll()
			}
		}
	}()

	return func() string {
		mu.Lock()
		defer mu.Unlock()

		return p
	}, cancel
}

// clientSend seals file to target: directly when the target is
// listening, else deposited at the storer. Direct is tried first with
// a short timeout; the storer is always the fallback. An interrupted
// deposit resumes at the chunk boundary the storer advertises.
// shaHex is the file's pre-computed SHA: it keys the deterministic
// file secret and the receiver's resume partial.
func clientSend(ctx context.Context, rwc io.ReadWriteCloser, id *peerID, targetName, path, shaHex string) error {
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

		// A failed direct attempt is retried within a short window
		// before the storer fallback: each redial resumes from the
		// listener's partial, so a target that vanished mid-transfer
		// and came back finishes with no storer bytes.
		deadline := time.Now().Add(directRetryWindow)

		resumed, dpath, err := directSend(ctx, tunnelClient(target.Addr, id.DialKey), directDial, id, target, f, info, path, shaHex)

		for err != nil && ctx.Err() == nil && time.Now().Before(deadline) {
			if errors.Is(err, errPartialBusy) {
				break // a pull is delivering this content: the storer already parks it
			}

			fmt.Fprintf(os.Stderr, "direct send failed (%v), retrying...\n", err)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(directBackoff):
			}

			// A restarted listener has the same address but fresh
			// peer state: it only re-adds us on a new meow
			// handshake, which the cached client never redoes
			// (its up flag is sticky) — so the retry dials on a
			// fresh, uncached client, patient enough for the
			// re-handshake.
			rc := &tailcat.Client{Server: target.Addr, Key: id.DialKey, Logf: func(string, ...any) {}}
			resumed, dpath, err = directSend(ctx, rc, directRetryDial, id, target, f, info, path, shaHex)
			_ = rc.Close()
		}

		if err == nil {
			fmt.Printf("sent %s to %s directly (%s%s%s)\n", filepath.Base(path), targetName, humanBytes(info.Size()), resumedSuffix(resumed), pathSuffix(dpath))

			return nil
		} else if ctx.Err() != nil {
			return ctx.Err() // Ctrl-C: the raw error is just the closed conn
		}
		// Target unreachable: fall through to the storer. The failed
		// direct attempt burned rwc's idle deadline, so try to redial
		// on the warm engine — through storerConn, whose watcher
		// keeps Ctrl-C able to abort the deposit. A failed redial
		// still deposits on the old conn.
		fmt.Fprintf(os.Stderr, "direct send failed (%v), depositing at the storer\n", err)

		if fresh, ferr := storerConn(ctx, id); ferr == nil {
			_ = rwc.Close()

			rwc = fresh
		}
	}

	if err := writeMsg(rwc, msg{Op: opSend, Target: targetName, FileName: filepath.Base(path), Size: info.Size(), SHA: shaHex}); err != nil {
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

	// Have as big as the file: the storer already parks this exact
	// content — the deposit is idempotent, no bytes to move.
	if info.Size() > 0 && m.Have >= info.Size() {
		if err := writeMsg(rwc, msg{Op: opSent, SHA: shaHex}); err != nil {
			return err
		}

		if m, err = readMsg(rwc); err != nil {
			return err
		}

		if m.Op != opDone || !m.OK {
			return fmt.Errorf("deposit failed: %s", m.Err)
		}

		fmt.Printf("sent %s to %s via storer (already parked, sha256 %s)\n", filepath.Base(path), targetName, shaHex[:12])

		return nil
	}

	// The storer may hold an interrupted deposit's partial: resume at
	// its chunk boundary like the direct path.
	resumed := resumeOffset(m.Have, info.Size())

	if _, err := f.Seek(resumed, io.SeekStart); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "depositing %s at the storer%s...\n", humanBytes(info.Size()-resumed), resumedSuffix(resumed))

	pathFn, stopPath := livePath(ctx, tunnelClient(id.StorerAddr, id.DialKey))
	defer stopPath()

	src := &progressReader{r: f, total: info.Size() - resumed, label: "depositing", every: time.Second, path: pathFn}

	plainSize, err := sealStream(id.Key, target.Key, rwc, src, shaHex, resumed)
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

	if err := writeMsg(rwc, msg{Op: opSent, SHA: shaHex}); err != nil {
		return err
	}

	m, err = readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opDone || !m.OK {
		return fmt.Errorf("deposit failed: %s", m.Err)
	}

	p := discoPath(ctx, tunnelClient(id.StorerAddr, id.DialKey))
	if s := pathFn(); s != "" {
		p = s // the poller watched this transfer's actual path
	}

	fmt.Printf("sent %s to %s via storer (%s, sha256 %s%s%s)\n", filepath.Base(path), targetName, humanBytes(info.Size()), shaHex[:12], resumedSuffix(resumed), pathSuffix(p))

	return nil
}

// tunnelClients keeps one tailcat client per address for the run: a
// fresh client redoes the DERP handshake, and re-handshaking the same
// key right after the previous session's close misses the 3s dial
// deadline (multi-file sends). Process exit tears it down.
var tunnelClients sync.Map

func tunnelClient(addr tailcat.Addr, dialKey key.NodePrivate) *tailcat.Client {
	c, _ := tunnelClients.LoadOrStore(string(addr), &tailcat.Client{Server: addr, Key: dialKey, Logf: func(string, ...any) {}})
	return c.(*tailcat.Client)
}

// directSend dials the target's listener and hands it the sealed
// file, resuming from the listener's advertised chunk boundary. It
// returns the offset it resumed from and the network path the
// transfer used. dialTimeout bounds the dial and, for a fresh client,
// its first meow handshake.
func directSend(ctx context.Context, c *tailcat.Client, dialTimeout time.Duration, id *peerID, target member, f *os.File, info os.FileInfo, path, shaHex string) (int64, string, error) {
	// A live listener answers its handshake in well under a second;
	// anything longer is a stale roster address — fail fast and let
	// the caller fall back to the storer.
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := c.DialTCPPort(dialCtx, catboxPort)
	if err != nil {
		return 0, "", err
	}

	defer func() { _ = conn.Close() }()

	// Like clientConn: Ctrl-C closes the conn to unblock a stuck
	// write — on the parent ctx, not the 3s dial timeout.
	go func() {
		<-ctx.Done()

		_ = conn.Close()
	}()

	pathFn, stopPath := livePath(ctx, c)
	defer stopPath()

	_ = conn.SetDeadline(time.Now().Add(directStall))

	if err := writeMsg(conn, msg{Op: opSend, Target: target.Name, FileName: filepath.Base(path), Size: info.Size(), SHA: shaHex}); err != nil {
		return 0, "", err
	}

	m, err := readMsg(conn)
	if err != nil {
		return 0, "", err
	}

	if m.Op != opReady || !m.OK {
		if m.Err == busyRefusal {
			return 0, "", fmt.Errorf("target refused: %w", errPartialBusy)
		}

		return 0, "", fmt.Errorf("target refused: %s", m.Err)
	}

	// The listener's partial decides where the stream continues; only
	// a chunk-aligned prefix of a smaller file can be trusted. The
	// seek is unconditional: a retry re-enters with the file at an
	// arbitrary offset.
	resumed := resumeOffset(m.Have, info.Size())

	if _, err := f.Seek(resumed, io.SeekStart); err != nil {
		return 0, "", err
	}

	src := &progressReader{r: f, total: info.Size() - resumed, label: "sending", every: time.Second, path: pathFn, onTick: func(int64) {
		_ = conn.SetDeadline(time.Now().Add(directStall)) // sliding: inactivity cap, not total
	}}

	plainSize, err := sealStream(id.Key, target.Key, conn, src, shaHex, resumed)
	src.close() // a failed write never reaches EOF: the line must not linger

	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err() // Ctrl-C: the raw error is just the closed conn
		}

		return 0, "", err
	}

	if plainSize != info.Size() {
		return 0, "", fmt.Errorf("sealed %d bytes but file is %d", plainSize, info.Size())
	}

	if err := writeMsg(conn, msg{Op: opSent, SHA: shaHex}); err != nil {
		return 0, "", err
	}

	m, err = readMsg(conn)
	if err != nil {
		return 0, "", err
	}

	if m.Op != opDone || !m.OK {
		return 0, "", fmt.Errorf("direct send failed: %s", m.Err)
	}

	return resumed, pathFn(), nil
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

// clientInbox pulls what the storer holds for this peer into dir: the
// named ids only, or everything when ids is empty. cl, when non-nil,
// lets each pull report its network path.
func clientInbox(ctx context.Context, rwc io.ReadWriteCloser, cl *tailcat.Client, id *peerID, dir string, ids []string) error {
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

	// The pull set: the named ids, or everything.
	pull := m.Items

	if len(ids) > 0 {
		want := make(map[string]bool, len(ids))
		for _, id := range ids {
			want[id] = true
		}

		got := make(map[string]bool, len(m.Items))
		pull = pull[:0]

		for _, it := range m.Items {
			if want[it.ID] {
				pull = append(pull, it)
			}

			got[it.ID] = true
		}

		var missing []string

		for _, id := range ids {
			if !got[id] {
				missing = append(missing, id)
			}
		}

		if len(missing) > 0 {
			return fmt.Errorf("no such pending item: %s", strings.Join(missing, ", "))
		}
	}

	var pullBytes int64

	for _, it := range pull {
		pullBytes += it.Plain
	}

	fmt.Fprintf(os.Stderr, "pulling %d files (%s)...\n", len(pull), humanBytes(pullBytes))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, it := range pull {
		if err := clientFetch(ctx, rwc, cl, id, it, dir); err != nil {
			if errors.Is(err, errPartialBusy) {
				fmt.Printf("skipped %s (already receiving)\n", it.FileName)

				continue // a direct receive holds it: leave it parked
			}

			return err
		}
	}

	return nil
}

// clientFetch pulls, verifies, and acknowledges one item. cl, when
// non-nil, reports the pull's network path. An interrupted pull keeps
// its partial (content-keyed by it.SHA): the next attempt resumes at
// its chunk boundary.
func clientFetch(ctx context.Context, rwc io.ReadWriteCloser, cl *tailcat.Client, id *peerID, it item, dir string) error {
	// One receiver per partial: a direct receive already writing this
	// exact content would interleave with ours into corruption.
	release, err := lockPartial(dir, it.SHA, it.Plain)
	if errors.Is(err, errPartialBusy) {
		return fmt.Errorf("%s: %w", it.FileName, errPartialBusy)
	}

	defer release()

	f, h, have, resumable, err := partialFor(dir, it.SHA, it.Plain)
	if err != nil {
		return err
	}

	// A resumable partial survives a failed attempt; a corrupt one
	// (SHA mismatch) and a plain temp do not.
	cleanup := !resumable
	done := false

	defer func() {
		_ = f.Close()

		if !done && cleanup {
			_ = os.Remove(f.Name())
		}
	}()

	if err := writeMsg(rwc, msg{Op: opFetch, ID: it.ID, Have: have}); err != nil {
		return err
	}

	m, err := readMsg(rwc)
	if err != nil {
		return err
	}

	if m.Op != opFile || !m.OK {
		return fmt.Errorf("fetch %s: %s", it.ID, m.Err)
	}

	// The storer arbitrates the resume: a rejected Have means the
	// partial cannot be trusted (or no longer matches) — start over.
	offered := have
	have = m.Have

	if offered > 0 && have == 0 {
		if err := f.Truncate(0); err != nil {
			return err
		}

		h.Reset()
	}

	dst := uniquePath(dir, m.FileName)

	// A live path for the progress lines when the pull knows its
	// client (tests pass nil).
	var pathFn func() string

	if cl != nil {
		var stopPath func()

		pathFn, stopPath = livePath(ctx, cl)
		defer stopPath()
	}

	src := &progressReader{r: io.LimitReader(rwc, m.Size), total: it.Plain, offset: have, label: "received", every: time.Second, path: pathFn}

	// The stream ends at its terminator; no size bound needed here.
	_, plainSize, sha, err := openStream(id.Key, src, f, have, h)
	src.close() // sealed streams self-terminate: no EOF reaches the reader

	_ = f.Close()

	if err != nil {
		return err
	}

	if m.SHA != "" && sha != m.SHA {
		cleanup = true // the partial's prefix is corrupt: failed for good

		return fmt.Errorf("%s: sha mismatch (got %s, want %s)", m.FileName, sha, m.SHA)
	}

	if err := os.Rename(f.Name(), dst); err != nil {
		return err
	}

	done = true

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

	p := ""
	if cl != nil {
		p = pathFn()

		if p == "" {
			p = discoPath(ctx, cl)
		}
	}

	fmt.Printf("got %s from %s (%s%s%s)\n", filepath.Base(dst), m.From, humanBytes(plainSize), resumedSuffix(have), pathSuffix(p))

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
