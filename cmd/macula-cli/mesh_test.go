package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/profile"
	"github.com/macula-io/macula-go/teststation"
)

// testMesh is two in-process macula 12 stations sharing a DHT, and a test
// realm with one org.
type testMesh struct {
	stations []*teststation.Station
	realm    teststation.Realm
}

func newTestMesh(t *testing.T) *testMesh {
	t.Helper()
	a := teststation.Start(t, profile.PQPure, "cli a")
	b := teststation.Start(t, profile.PQPure, "cli b")
	teststation.ShareDHT(a, b)
	return &testMesh{stations: []*teststation.Station{a, b}, realm: teststation.NewRealm(t, profile.PQPure, "cli", "mcl-cli")}
}

// flags are the mesh flags a node on station i passes, trusting the realm when
// trusted.
func (tm *testMesh) flags(i int, trusted bool) *meshFlags {
	s := tm.stations[i]
	m := &meshFlags{seeds: seedList{{Host: s.Host, Port: s.Port, NodeID: s.NodeID}},
		realm: hex.EncodeToString(tm.realm.ID[:]), profile: "pq_pure", timeout: 10 * time.Second}
	if trusted {
		m.realmKey = hex.EncodeToString(tm.realm.RealmKey())
	}
	return m
}

// node is a pool on station i with a fresh key, admitted by the realm's org
// when admitted.
func (tm *testMesh) node(t *testing.T, i int, trusted, admitted bool) (*pool.Pool, *meshFlags) {
	t.Helper()
	key := freshKey(t)
	if admitted {
		id, _ := key.NodeID()
		tm.realm.Admit(t, tm.stations[i], id)
	}
	m := tm.flags(i, trusted)
	p, err := m.connect(context.Background(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, m
}

var keyCount atomic.Int64

// freshKey is a key no other node in the test binary has: teststation.Key
// gives one key per name, and a station drops the older of two connections
// under one identity.
func freshKey(t *testing.T) *identity.NodeKey {
	t.Helper()
	return teststation.Key(t, profile.PQPure, fmt.Sprintf("%s/%d", t.Name(), keyCount.Add(1)))
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
