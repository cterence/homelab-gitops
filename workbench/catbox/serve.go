package main

// The storer: one tailcat listener, the roster authority, the spool.
// Join is authenticated by address possession: anyone who can open a
// tunnel (PeerKey) and isn't yet a member may join with a fresh name.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

// catboxPort is the single tailcat TCP port the protocol speaks on.
const catboxPort = 2690

type storer struct {
	dir string
	srv *tailcat.Server
	log *slog.Logger
	max int64
	ttl time.Duration

	mu     sync.Mutex
	roster []member
	spool  *spool
}

func runServe(ctx context.Context, log *slog.Logger, dataDir string, region int64, max int64, ttl time.Duration, healthAddr string) error {
	id, created, err := loadStorerIdentity(ctx, dataDir, region)
	if err != nil {
		return err
	}

	if created {
		log.Info("identity created", "dir", dataDir)
	}

	roster, err := loadRoster(rosterPath(dataDir))
	if err != nil {
		return err
	}

	sp, err := openSpool(spoolDir(dataDir))
	if err != nil {
		return err
	}

	st := &storer{
		dir:    dataDir,
		log:    log,
		max:    max,
		ttl:    ttl,
		roster: roster,
		spool:  sp,
	}

	st.srv = &tailcat.Server{
		Key:          id.Private,
		PresharedKey: id.Public.PresharedKey,
		RegionID:     id.Public.RegionID,
		Logf:         func(format string, args ...any) { log.Debug(fmt.Sprintf(format, args...)) },
		OnTCP:        st.onTCP,
	}
	if err := st.srv.Start(); err != nil {
		return fmt.Errorf("starting tailcat: %w", err)
	}

	defer func() { _ = st.srv.Close() }()

	log.Info("listening", "port", catboxPort, "members", len(st.roster)) // no address: it's a capability

	health := startHealth(log, healthAddr)

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = health.Shutdown(shutdownCtx)
	}()

	if err := st.spool.sweep(st.ttl); err != nil {
		log.Warn("spool sweep failed", "err", err)
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := st.spool.sweep(st.ttl); err != nil {
				log.Warn("spool sweep failed", "err", err)
			}
		}
	}
}

func spoolDir(dataDir string) string { return dataDir + "/spool" }

// onTCP answers only the protocol port.
func (st *storer) onTCP(port uint16) func(net.Conn) {
	if port != catboxPort {
		return nil
	}

	return st.handleConn
}

func (st *storer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	peer, ok := st.srv.PeerKey(conn.RemoteAddr())
	if !ok {
		return // no authenticated peer key, nothing to say
	}

	// ponytail: one hour covers the largest deposit over DERP
	_ = conn.SetDeadline(time.Now().Add(time.Hour))
	st.serveConn(conn, peer)
}

// serveConn runs protocol ops on one connection until it closes; split
// from handleConn so tests can drive it over a pipe.
func (st *storer) serveConn(rwc io.ReadWriteCloser, peer key.NodePublic) {
	for {
		m, err := readMsg(rwc)
		if err != nil {
			return
		}

		me, isMember := st.lookupMember(peer)
		switch {
		case m.Op == opJoin:
			st.opJoin(rwc, peer, me, isMember, m)
		case !isMember:
			_ = writeMsg(rwc, msg{Op: opDone, Err: "not a member; run catbox join"})

			return
		case m.Op == opSend:
			st.opSend(rwc, me, m)
		case m.Op == opPending:
			st.opPending(rwc, me)
		case m.Op == opFetch:
			st.opFetch(rwc, me, m)
		default:
			_ = writeMsg(rwc, msg{Op: opDone, Err: "unknown op " + m.Op})
		}
	}
}

func (st *storer) lookupMember(k key.NodePublic) (m member, ok bool) {
	st.mu.Lock()
	defer st.mu.Unlock()

	m, ok = memberByDialKey(st.roster, k)

	return m, ok
}

func (st *storer) members() []member {
	st.mu.Lock()
	defer st.mu.Unlock()

	return append([]member(nil), st.roster...)
}

func (st *storer) opJoin(rwc io.ReadWriteCloser, peer key.NodePublic, cur member, isMember bool, m msg) {
	if isMember {
		// Re-join refreshes the identity key and listener address.
		if m.Name != "" && m.Name != cur.Name {
			_ = writeMsg(rwc, msg{Op: opJoined, Err: "already a member as " + cur.Name})

			return
		}

		if ident, kerr := parseNodeKey(m.Key); kerr == nil {
			st.mu.Lock()

			for i := range st.roster {
				if st.roster[i].Name == cur.Name {
					st.roster[i].Key = ident
					st.roster[i].Addr = tailcat.Addr(m.Addr)
				}
			}

			members := append([]member(nil), st.roster...)
			st.mu.Unlock()

			if err := saveRoster(rosterPath(st.dir), members); err != nil {
				st.log.Error("saving roster", "err", err)
			}
		}

		_ = writeMsg(rwc, msg{Op: opJoined, OK: true, Members: st.members()})

		return
	}

	if !validName(m.Name) {
		_ = writeMsg(rwc, msg{Op: opJoined, Err: "name must be a lowercase slug"})

		return
	}

	if _, dup := memberByName(st.members(), m.Name); dup {
		_ = writeMsg(rwc, msg{Op: opJoined, Err: "name already taken"})

		return
	}

	ident, err := parseNodeKey(m.Key)
	if err != nil {
		_ = writeMsg(rwc, msg{Op: opJoined, Err: "join requires the identity public key"})

		return
	}

	st.mu.Lock()
	st.roster = append(st.roster, member{Name: m.Name, Key: ident, DialKey: peer, Addr: tailcat.Addr(m.Addr), Joined: time.Now().Unix()})
	members := append([]member(nil), st.roster...)
	st.mu.Unlock()

	if err := saveRoster(rosterPath(st.dir), members); err != nil {
		st.log.Error("saving roster", "err", err)
	}

	st.log.Info("member joined", "name", m.Name, "listening", m.Addr != "")

	_ = writeMsg(rwc, msg{Op: opJoined, OK: true, Members: members})
}

func (st *storer) opSend(rwc io.ReadWriteCloser, me member, m msg) {
	target, ok := memberByName(st.members(), m.Target)
	if !ok {
		_ = writeMsg(rwc, msg{Op: opReady, Err: "unknown target " + m.Target, Members: st.members()})

		return
	}

	usage, err := st.spool.usage()
	if err != nil {
		_ = writeMsg(rwc, msg{Op: opReady, Err: "spool unavailable"})

		return
	}

	if usage+m.Size > st.max {
		_ = writeMsg(rwc, msg{Op: opReady, Err: "spool full"})

		return
	}

	if err := writeMsg(rwc, msg{Op: opReady, OK: true, Members: st.members()}); err != nil {
		return
	}

	meta := spoolMeta{FileName: m.FileName, From: me.Name, Target: target.Name}

	meta, err = st.spool.put(rwc, meta)
	if err != nil {
		st.log.Warn("deposit failed", "err", err)
		return
	}
	// The sender's done message carries the plaintext SHA for the receiver.
	done, err := readMsg(rwc)
	if err != nil || done.Op != opSent {
		return
	}

	st.patchMeta(meta.ID, func(sm *spoolMeta) { sm.SHA = done.SHA })
	st.log.Info("deposited", "id", meta.ID, "target", target.Name, "bytes", meta.Size)

	_ = writeMsg(rwc, msg{Op: opDone, OK: true})
}

// patchMeta rewrites one sidecar field.
func (st *storer) patchMeta(id string, f func(*spoolMeta)) {
	path := st.spool.metaPath(id)

	sm := spoolMeta{}
	if err := loadJSON(path, &sm); err != nil {
		return
	}

	f(&sm)

	_ = saveJSON(path, sm)
}

func (st *storer) opPending(rwc io.ReadWriteCloser, me member) {
	metas, err := st.spool.items()
	if err != nil {
		_ = writeMsg(rwc, msg{Op: opItems, Err: "spool unavailable"})

		return
	}

	var items []item

	for _, sm := range metas {
		if sm.Target != me.Name {
			continue
		}

		items = append(items, item{ID: sm.ID, FileName: sm.FileName, SHA: sm.SHA, From: sm.From, Size: sm.Size})
	}

	_ = writeMsg(rwc, msg{Op: opItems, OK: true, Items: items, Members: st.members()})
}

func (st *storer) opFetch(rwc io.ReadWriteCloser, me member, m msg) {
	meta, blob, err := st.spool.open(m.ID)
	if err != nil || meta.Target != me.Name {
		_ = writeMsg(rwc, msg{Op: opFile, Err: "no such item"})

		return
	}

	defer func() { _ = blob.Close() }()

	if err := writeMsg(rwc, msg{Op: opFile, OK: true, FileName: meta.FileName, SHA: meta.SHA, From: meta.From, Size: meta.Size}); err != nil {
		return
	}

	if _, err := io.Copy(rwc, blob); err != nil {
		st.log.Warn("serving blob failed", "id", m.ID, "err", err)
		return
	}

	ack, err := readMsg(rwc)
	if err != nil || ack.Op != opAck || ack.ID != m.ID {
		return
	}
	// ponytail: trust the receiver's ack; SHA was verified client-side
	if err := st.spool.delete(m.ID); err != nil {
		st.log.Warn("delete after ack failed", "id", m.ID, "err", err)
	}

	st.log.Info("delivered", "id", m.ID, "to", me.Name)

	_ = writeMsg(rwc, msg{Op: opAcked, OK: true})
}

// startHealth serves /healthz for probes.
func startHealth(log *slog.Logger, addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health server failed", "err", err)
		}
	}()

	return srv
}
