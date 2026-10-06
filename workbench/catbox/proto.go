package main

// Wire messages: one JSON object per 4-byte big-endian length-framed
// frame over the tailcat stream. A single struct carries every op.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

const (
	opJoin    = "join"    // peer: {Name} admit me
	opJoined  = "joined"  // storer: {OK, Err, Members}
	opSend    = "send"    // peer: {Target, FileName, Size} then sealed stream
	opReady   = "ready"   // storer: {OK, Err, Members}
	opSent    = "sent"    // peer: {SHA} stream finished
	opDone    = "done"    // storer: {OK, Err}
	opPending = "pending" // peer: what's held for me
	opItems   = "items"   // storer: {Items, Members}
	opFetch   = "fetch"   // peer: {ID}
	opFile    = "file"    // storer: {OK, FileName, SHA, From, Size} then sealed stream
	opAck     = "ack"     // peer: {ID} received and verified
	opAcked   = "acked"   // storer: {OK, Err}
)

const msgMax = 1 << 20

// msg is the wire message; Op dispatches. Size means plaintext bytes
// for send. Key and Addr carry the joiner's identity public key and
// listener address.
type msg struct {
	Op       string   `json:"op"`
	Name     string   `json:"name,omitempty"`
	Key      string   `json:"key,omitempty"`
	Addr     string   `json:"addr,omitempty"`
	Target   string   `json:"target,omitempty"`
	FileName string   `json:"fn,omitempty"`
	ID       string   `json:"id,omitempty"`
	SHA      string   `json:"sha,omitempty"`
	From     string   `json:"from,omitempty"`
	Err      string   `json:"err,omitempty"`
	Size     int64    `json:"size,omitempty"`
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
// the listener's tailcat address, empty when not listening.
type member struct {
	Name    string         `json:"name"`
	Key     key.NodePublic `json:"key"`
	DialKey key.NodePublic `json:"dial_key"`
	Addr    tailcat.Addr   `json:"addr,omitempty"`
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
