package main

// The listener: an explicitly online peer that receives files
// directly. Serves only sends addressed to itself; anything else the
// sender falls back to the storer for.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// runRecv is the peer's two receive verbs: pull what the storer holds
// — everything, or the named ids only — or listen for direct sends.
// Listening never pulls — files park at the storer until the peer
// asks for them.
func runRecv(ctx context.Context, log *slog.Logger, inboxDir string, listen bool, ids []string) error {
	id, _, err := loadPeerID("", "")
	if err != nil {
		return err
	}

	sweepPartials(inboxDir)

	if !listen {
		release, err := lockPeer(ctx)
		if err != nil {
			return err
		}

		defer release()

		conn, cl, err := clientConn(ctx, id)
		if err != nil {
			return err
		}

		defer func() { _ = cl.Close() }()
		defer func() { _ = conn.Close() }()

		return clientInbox(ctx, conn, cl, id, inboxDir, ids)
	}

	if err := ensureListenerIdentity(ctx, id); err != nil {
		return err
	}

	lc := &listener{inbox: inboxDir, log: log, id: id}

	srv := &tailcat.Server{
		Key:          id.Key,
		PresharedKey: id.PSK,
		RegionID:     regionOf(id),
		Logf:         func(format string, args ...any) { log.Debug(fmt.Sprintf(format, args...)) },
		OnTCP:        lc.onTCP,
	}
	lc.srv = srv

	if err := srv.Start(); err != nil {
		return fmt.Errorf("starting tailcat: %w", err)
	}

	if err := registerListener(ctx, id, srv.TailcatAddr()); err != nil {
		_ = srv.Close()

		return err
	}

	log.Info("listening", "port", catboxPort, "inbox", inboxDir) // no address: it's a capability

	<-ctx.Done()

	// Close the server before deregistering: in-flight transfers die
	// now, so a shut-down listener makes senders fall back to the
	// storer immediately instead of stalling until their idle
	// deadline.
	if err := srv.Close(); err != nil {
		log.Warn("closing listener failed", "err", err)
	}

	// Deregister so senders don't waste their direct-dial timeout on us.
	deregCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if err := registerListener(deregCtx, id, ""); err != nil {
		log.Warn("deregistering listener address failed", "err", err)
	}

	return nil
}

// registerListener joins the storer, publishing (or clearing) our address.
func registerListener(ctx context.Context, id *peerID, addr tailcat.Addr) error {
	conn, cl, err := clientConn(ctx, id)
	if err != nil {
		return err
	}

	defer func() { _ = cl.Close() }()

	defer func() { _ = conn.Close() }()

	return joinReq(conn, id, addr, "")
}

// ensureListenerIdentity bakes in a PSK and DERP region on first listen.
func ensureListenerIdentity(ctx context.Context, id *peerID) error {
	if !id.PSK.IsZero() && id.Region != 0 {
		return nil
	}

	if id.PSK.IsZero() {
		id.PSK = tailcat.NewPresharedKey()
	}

	if id.Region == 0 {
		region, err := pickRegion(ctx)
		if err != nil {
			return fmt.Errorf("picking DERP region: %w", err)
		}

		id.Region = region
	}

	path := filepath.Join(peerConfigDir(), "identity.json")

	return saveJSON(path, id)
}

func regionOf(id *peerID) tailcfg.DERPRegionID { return tailcfg.DERPRegionID(id.Region) }

// senderName resolves a sender identity key to a member name from the
// roster cache, or "unknown" when the cache predates the sender.
func senderName(sender key.NodePublic) string {
	roster, err := loadRoster(rosterPath(peerConfigDir()))
	if err != nil {
		return "unknown"
	}

	if m, ok := memberByIdentityKey(roster, sender); ok {
		return m.Name
	}

	return "unknown"
}

type listener struct {
	inbox string
	log   *slog.Logger
	id    *peerID
	srv   *tailcat.Server // set by runRecv; nil in tests, silencing the path report
}

// senderPath reports the network path a sender's transfer used; a nil
// srv (tests) reports nothing.
func (lc *listener) senderPath(sender key.NodePublic) string {
	if lc.srv == nil {
		return ""
	}

	return pathOf(lc.srv.Status(), sender)
}

func (lc *listener) onTCP(port uint16) func(net.Conn) {
	if port != catboxPort {
		return nil
	}

	return lc.handleConn
}

func (lc *listener) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }() // ponytail: address possession is the capability

	// The direct send's silence cap, mirrored: a vanished sender ends
	// the receive at the same cap the sender aborts at, instead of
	// riding the storer paths' longer idle deadline.
	_ = conn.SetDeadline(time.Now().Add(directStall))
	lc.listenConn(conn)
}

// listenConn receives one or more direct sends; split for tests.
func (lc *listener) listenConn(rwc io.ReadWriteCloser) {
	for {
		m, err := readMsg(rwc)
		if err != nil {
			return
		}

		if m.Op != opSend {
			_ = writeMsg(rwc, msg{Op: opDone, Err: "listener only takes sends"})
			continue
		}

		if m.Target != lc.id.Name {
			_ = writeMsg(rwc, msg{Op: opReady, Err: "not addressed to this peer"})

			continue
		}

		lc.receive(rwc, m)
	}
}

// receive takes one sealed stream to the inbox, resuming from this
// transfer's partial when there is one.
func (lc *listener) receive(rwc io.ReadWriteCloser, m msg) {
	if err := os.MkdirAll(lc.inbox, 0o755); err != nil {
		_ = writeMsg(rwc, msg{Op: opDone, Err: err.Error()})

		return
	}

	// One receiver per partial: a pull already delivering this exact
	// content would interleave with ours into corruption. Decline, and
	// the sender falls back to the storer where the identical item is
	// already parked.
	release, err := lockPartial(lc.inbox, m.SHA, m.Size)
	if errors.Is(err, errPartialBusy) {
		_ = writeMsg(rwc, msg{Op: opReady, Err: busyRefusal})

		return
	}

	defer release()

	f, h, have, resumable, err := partialFor(lc.inbox, m.SHA, m.Size)
	if err != nil {
		_ = writeMsg(rwc, msg{Op: opDone, Err: err.Error()})

		return
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

	// Announce before the bytes move: the CLI log and the app's row
	// both come from this plain stderr line. The name is resolved
	// from the roster by peer key when the engine knows it — a
	// claimed name on the wire is display-only, never an identity.
	from := m.From

	if conn, ok := rwc.(net.Conn); ok && lc.srv != nil {
		if peer, pok := lc.srv.PeerKey(conn.RemoteAddr()); pok {
			if name := senderName(peer); name != "unknown" {
				from = name
			}
		}
	}

	if from == "" {
		from = "unknown peer"
	}

	fmt.Fprintf(os.Stderr, "%s is sending %s directly (%s%s)\n", from, m.FileName, humanBytes(m.Size), resumedSuffix(have))

	// The sender's cache rides the direct path: hand it our roster
	// view so it stays fresh without stash contact.
	members, _ := loadRoster(rosterPath(peerConfigDir()))

	if err := writeMsg(rwc, msg{Op: opReady, OK: true, Have: have, Members: members}); err != nil {
		return
	}

	// The live path for the progress lines: this conn's peer, read
	// passively from the engine status — the sender is the one
	// pinging; the listener just watches its own state.
	var pathFn func() string

	if conn, ok := rwc.(net.Conn); ok && lc.srv != nil {
		if peer, ok := lc.srv.PeerKey(conn.RemoteAddr()); ok {
			pathFn = func() string { return pathOf(lc.srv.Status(), peer) }
		}
	}

	// The stream ends at its terminator; no size bound needed here.
	// Ticks print progress (the app's log pane sees plain stderr) and
	// slide the conn deadline.
	src := &progressReader{r: rwc, total: m.Size, offset: have, label: "received", every: time.Second, path: pathFn, onTick: func(int64) {
		if c, ok := rwc.(net.Conn); ok {
			_ = c.SetDeadline(time.Now().Add(directStall)) // sliding: inactivity cap, like the sender's
		}
	}}

	sender, plainSize, sha, err := openStream(lc.id.Key, src, f, have, h)
	src.close() // sealed streams self-terminate: no EOF ever reaches the reader

	_ = f.Close()

	if err != nil {
		lc.log.Warn("direct receive failed", "err", err)

		// Plain line so the app's indicator clears instead of freezing.
		fmt.Fprintf(os.Stderr, "receive failed: %v\n", err)

		_ = writeMsg(rwc, msg{Op: opDone, Err: "corrupt sealed stream"})

		return
	}

	sent, err := readMsg(rwc)
	if err != nil || sent.Op != opSent {
		return
	}

	if sent.SHA != "" && sent.SHA != sha {
		// Reply only after cleanup so senders never observe stale temps.
		cleanup = true // the partial's prefix is corrupt: failed for good
		_ = os.Remove(f.Name())
		_ = writeMsg(rwc, msg{Op: opDone, Err: "sha mismatch"})

		return
	}

	dst := uniquePath(lc.inbox, m.FileName)
	if err := os.Rename(f.Name(), dst); err != nil {
		_ = writeMsg(rwc, msg{Op: opDone, Err: err.Error()})

		return
	}

	done = true

	p := lc.senderPath(sender)
	lc.log.Info("received directly", "filename", filepath.Base(dst), "from", senderName(sender), "size", humanBytes(plainSize), "resumed", have, "path", p)

	// Plain line for humans and the app's pane (slog lines are noise
	// there): also clears the in-UI transfer indicator.
	fmt.Fprintf(os.Stderr, "got %s directly (%s%s%s)\n", filepath.Base(dst), humanBytes(plainSize), resumedSuffix(have), pathSuffix(p))

	_ = writeMsg(rwc, msg{Op: opDone, OK: true})
}
