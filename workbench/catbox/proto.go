package main

// Wire messages: one JSON object per 4-byte big-endian length-framed
// frame over the tailcat stream. A single struct carries every op.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

const (
	opJoin    = "join"    // peer: {Name} admit me
	opJoined  = "joined"  // storer: {OK, Err, Members}
	opSend    = "send"    // peer: {Target, FileName, Size, SHA} then sealed stream
	opReady   = "ready"   // receiver: {OK, Err, Have, Members}
	opSent    = "sent"    // peer: {SHA} stream finished
	opDone    = "done"    // receiver: {OK, Err}
	opPending = "pending" // peer: what's held for me
	opItems   = "items"   // storer: {Items, Members}
	opFetch   = "fetch"   // peer: {ID, Have} pull from my chunk boundary
	opFile    = "file"    // storer: {OK, FileName, SHA, From, Size} then sealed stream
	opAck     = "ack"     // peer: {ID} received and verified
	opAcked   = "acked"   // storer: {OK, Err}
	opDismiss = "dismiss" // peer: {ID} refuse delivery of my own pending item
	opRemove  = "remove"  // peer: {Target} drop a member from the roster
	opInvite  = "invite"  // admin: mint a one-time join code
	opInvited = "invited" // storer: {OK, Err, Code}
)

const msgMax = 1 << 20

// idleTimeout is the transfer inactivity cap: bytes flowing extend the
// conn deadline (see the progress ticks), silence ends it — a vanished
// peer or a dead network aborts a stuck transfer within this bound
// instead of hanging until human intervention.
const idleTimeout = 2 * time.Minute

// directStall caps a direct transfer's silence tighter than the
// storer paths: a vanished listener must be detected fast enough that
// the storer fallback still helps, and a resume makes the abort
// cheap.
const directStall = 30 * time.Second

// directRetryWindow bounds how long a failed direct attempt is
// retried before the storer fallback: every redial resumes from the
// listener's partial, so a target that vanished mid-transfer and
// comes back within it finishes direct with no storer bytes.
var directRetryWindow = 2 * time.Minute

// directBackoff paces the direct redials inside the retry window.
var directBackoff = 5 * time.Second

// directDial bounds the first direct dial: a live listener answers
// its handshake in well under a second, so this is the stale-address
// detector.
var directDial = 3 * time.Second

// directRetryDial bounds a retry's dial: it rides a fresh client, so
// it must also cover the meow re-handshake with a listener that
// restarted.
var directRetryDial = 15 * time.Second

func init() {
	// Test seam: the integration test's helper children shrink the
	// retry patience, so a dead listener falls back to the storer in
	// about a second instead of after the real window.
	if os.Getenv("CATBOX_FAST_TIMERS") == "1" {
		directRetryWindow = 500 * time.Millisecond
		directBackoff = 100 * time.Millisecond
		directDial = time.Second
		directRetryDial = time.Second
	}
}

// msg is the wire message; Op dispatches. Size means plaintext bytes
// for send. Key and Addr carry the joiner's identity public key and
// listener address. Code carries the join's one-time invite.
type msg struct {
	Op       string   `json:"op"`
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key,omitempty"`
	Addr     string   `json:"addr,omitempty"`
	Code     string   `json:"code,omitempty"`
	Target   string   `json:"target,omitempty"`
	FileName string   `json:"fn,omitempty"`
	ID       string   `json:"id,omitempty"`
	SHA      string   `json:"sha,omitempty"`
	From     string   `json:"from,omitempty"`
	Err      string   `json:"err,omitempty"`
	Size     int64    `json:"size,omitempty"`
	Have     int64    `json:"have,omitempty"` // chunk-aligned plaintext bytes the receiver already holds
	OK       bool     `json:"ok,omitempty"`
	Items    []item   `json:"items,omitempty"`
	Members  []member `json:"members,omitempty"`
}

// item is one file held at the storer. Plain is for display; Size
// (sealed) bounds the transfer.
type item struct {
	ID       string `json:"id"`
	FileName string `json:"fn"`
	SHA      string `json:"sha"`
	From     string `json:"from"`
	Plain    int64  `json:"plain"`
	Size     int64  `json:"size"`
}

// member is one admitted machine. Key is the identity (and listener)
// public key; DialKey is what the storer sees on connections. Addr is
// the listener's tailcat address, empty when not listening. Admin
// members mint invites and remove other members.
type member struct {
	Name    string         `json:"name"`
	Key     key.NodePublic `json:"key"`
	DialKey key.NodePublic `json:"dial_key"`
	Addr    tailcat.Addr   `json:"addr,omitempty"`
	Admin   bool           `json:"admin,omitempty"`
	Joined  int64          `json:"joined"` // unix seconds
}

func writeMsg(w io.Writer, m msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", m.Op, err)
	}

	var frame [4]byte
	binary.BigEndian.PutUint32(frame[:], uint32(len(b)))

	if _, err := w.Write(frame[:]); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("write %s: %w", m.Op, err)
	}

	return nil
}

func readMsg(r io.Reader) (msg, error) {
	var frame [4]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil {
		return msg{}, fmt.Errorf("read frame: %w", err)
	}

	n := binary.BigEndian.Uint32(frame[:])
	if n > msgMax {
		return msg{}, fmt.Errorf("message of %d bytes exceeds %d", n, msgMax)
	}

	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return msg{}, fmt.Errorf("read message: %w", err)
	}

	var m msg
	if err := json.Unmarshal(b, &m); err != nil {
		return msg{}, fmt.Errorf("unmarshal: %w", err)
	}

	return m, nil
}
