package daemon

import (
	"bufio"
	"net"
	"testing"
	"time"
)

// watchForDisconnect must tolerate exactly the one benign leftover
// byte json.Decoder.Decode is known to leave behind -- see its own
// doc comment for the full mechanism, and the receipts (a 74-byte
// pubsub topic name reproduced this live against a real daemon; 73
// bytes worked, 74 didn't, depending only on where the request's own
// trailing '\n' landed relative to Decode()'s internal read chunking).

func TestWatchForDisconnect_TrailingNewlineIsNotADisconnect(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	br := bufio.NewReader(serverSide)
	disconnected := watchForDisconnect(br)

	// Simulates the exact leftover json.Encoder.Encode always produces:
	// Decode() consumed the JSON body, this trailing '\n' is still
	// sitting on the wire unread when watchForDisconnect starts.
	go func() { _, _ = clientSide.Write([]byte("\n")) }()

	select {
	case <-disconnected:
		t.Fatal("watchForDisconnect treated the request's own trailing newline as a disconnect")
	case <-time.After(200 * time.Millisecond):
		// correct: still watching, exactly as if nothing had arrived
	}
}

func TestWatchForDisconnect_RealByteIsADisconnect(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	br := bufio.NewReader(serverSide)
	disconnected := watchForDisconnect(br)

	go func() { _, _ = clientSide.Write([]byte("x")) }()

	select {
	case <-disconnected:
		// correct: a real, unexpected byte means something is wrong on
		// this connection (the client should never send anything else)
	case <-time.After(1 * time.Second):
		t.Fatal("watchForDisconnect never fired for a genuine unexpected byte")
	}
}

func TestWatchForDisconnect_CloseIsADisconnect(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()

	br := bufio.NewReader(serverSide)
	disconnected := watchForDisconnect(br)

	_ = clientSide.Close()

	select {
	case <-disconnected:
		// correct
	case <-time.After(1 * time.Second):
		t.Fatal("watchForDisconnect never fired when the client actually closed the connection")
	}
}

// The exact bug shape: a leftover newline followed, much later, by a
// real disconnect must still eventually fire -- confirms the loop
// doesn't get stuck tolerating newlines forever.
func TestWatchForDisconnect_NewlineThenRealDisconnectStillFires(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()

	br := bufio.NewReader(serverSide)
	disconnected := watchForDisconnect(br)

	go func() {
		_, _ = clientSide.Write([]byte("\n"))
		time.Sleep(20 * time.Millisecond)
		_ = clientSide.Close()
	}()

	select {
	case <-disconnected:
		// correct
	case <-time.After(1 * time.Second):
		t.Fatal("watchForDisconnect never fired after the leftover newline was followed by a real disconnect")
	}
}
