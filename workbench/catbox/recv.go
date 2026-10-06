package main

// The listener: an explicitly online peer that receives files
// directly. Serves only sends addressed to itself; anything else the
// sender falls back to the storer for.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

// runRecv is the peer's single receive verb: pull everything the
// storer holds, then optionally stay online for direct sends, with the
// listener address registered while staying and cleared on exit.
func runRecv(ctx context.Context, log *slog.Logger, inboxDir string, stay bool) error {
	id, _, err := loadPeerID("", "")
	if err != nil {
		return err
	}

	conn, cl, err := clientConn(ctx, id)
	if err != nil {
		return err
	}

	err = clientInbox(ctx, conn, id, inboxDir)
	_ = cl.Close()
	_ = conn.Close()

	if err != nil {
		return err
	}

	if !stay {
		return nil
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
	if err := srv.Start(); err != nil {
		return fmt.Errorf("starting tailcat: %w", err)
	}

	defer func() { _ = srv.Close() }()

	if err := registerListener(ctx, id, srv.TailcatAddr()); err != nil {
		return err
	}

	log.Info("listening", "port", catboxPort, "inbox", inboxDir) // no address: it's a capability

	<-ctx.Done()

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

	return joinReq(conn, id, addr)
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

type listener struct {
	inbox string
	log   *slog.Logger
	id    *peerID
}

func (lc *listener) onTCP(port uint16) func(net.Conn) {
	if port != catboxPort {
		return nil
	}

	return lc.handleConn
}

func (lc *listener) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }() // ponytail: address possession is the capability

	_ = conn.SetDeadline(time.Now().Add(time.Hour))
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

// receive takes one sealed stream to the inbox.
func (lc *listener) receive(rwc io.ReadWriteCloser, m msg) {
	if err := writeMsg(rwc, msg{Op: opReady, OK: true}); err != nil {
		return
	}

	if err := os.MkdirAll(lc.inbox, 0o755); err != nil {
		_ = writeMsg(rwc, msg{Op: opDone, Err: err.Error()})

		return
	}

	tmp, err := os.CreateTemp(lc.inbox, ".part-*")
	if err != nil {
		_ = writeMsg(rwc, msg{Op: opDone, Err: err.Error()})

		return
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	// The stream ends at its terminator; no size bound needed here.
	_, sha, err := openStream(lc.id.Key, rwc, tmp)
	_ = tmp.Close()

	if err != nil {
		lc.log.Warn("direct receive failed", "err", err)

		_ = writeMsg(rwc, msg{Op: opDone, Err: "corrupt sealed stream"})

		return
	}

	sent, err := readMsg(rwc)
	if err != nil || sent.Op != opSent {
		return
	}

	if sent.SHA != "" && sent.SHA != sha {
		// Reply only after cleanup so senders never observe stale temps.
		_ = os.Remove(tmp.Name())
		_ = writeMsg(rwc, msg{Op: opDone, Err: "sha mismatch"})

		return
	}

	if err := os.Rename(tmp.Name(), filepath.Join(lc.inbox, m.FileName)); err != nil {
		_ = writeMsg(rwc, msg{Op: opDone, Err: err.Error()})

		return
	}

	lc.log.Info("received directly", "fn", m.FileName, "from", "peer")

	_ = writeMsg(rwc, msg{Op: opDone, OK: true})
}
