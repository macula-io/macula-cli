package main

import (
	"encoding/binary"
	"testing"

	"github.com/macula-io/macula-go/identity"
)

// proofMessage's byte layout must match mailbox_ownership_proof:message/3
// and citizen_ownership_proof:message/3 on the Erlang side EXACTLY:
// node_id (32 raw bytes) ++ timestamp (8 bytes, big-endian) ++ procedure
// (raw UTF-8 bytes), no delimiters. This test pins that layout so a
// change here can't silently break interop with either service.
func TestProofMessageLayout(t *testing.T) {
	nodeID := make([]byte, 32)
	for i := range nodeID {
		nodeID[i] = byte(i)
	}
	got := proofMessage(nodeID, 0x0102030405060708, "hecate_mail.get_mailbox")

	if len(got) != 32+8+len("hecate_mail.get_mailbox") {
		t.Fatalf("length = %d, want %d", len(got), 32+8+len("hecate_mail.get_mailbox"))
	}
	for i := 0; i < 32; i++ {
		if got[i] != byte(i) {
			t.Fatalf("node_id byte %d = %#x, want %#x", i, got[i], byte(i))
		}
	}
	if ts := binary.BigEndian.Uint64(got[32:40]); ts != 0x0102030405060708 {
		t.Fatalf("timestamp = %#x, want %#x", ts, uint64(0x0102030405060708))
	}
	if string(got[40:]) != "hecate_mail.get_mailbox" {
		t.Fatalf("procedure = %q, want %q", got[40:], "hecate_mail.get_mailbox")
	}
}

// A signature over proofMessage must verify against the SAME node_id
// with macula-go's own identity.Verify -- the exact check
// mailbox_ownership_proof:verify/3 performs server-side via
// macula_identity:verify/3, just exercised from this side of the wire.
func TestProofSignatureVerifies(t *testing.T) {
	kp, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	msg := proofMessage(kp.NodeID(), 1_700_000_000_000, "hecate_citizens.register_presence")
	sig := kp.Sign(msg)

	if !identity.Verify(kp.NodeID(), msg, sig) {
		t.Fatal("signature did not verify against its own node_id")
	}

	other, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	if identity.Verify(other.NodeID(), msg, sig) {
		t.Fatal("signature verified against a DIFFERENT node_id -- should not")
	}
}
