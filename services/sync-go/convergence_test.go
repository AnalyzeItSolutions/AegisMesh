package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	pb "github.com/aegismesh/aegismesh/gen/aegismeshv1"
	crdt "snippet/crdt"
)

// 50 clients edit offline, then sync through the real server logic over a link that loses 20% of the calls.
const (
	simClients = 50
	simEdits   = 1000
	simLoss    = 0.20
	simKeys    = 10
)

type simClient struct {
	id  string
	hlc *crdt.HLC
	ts  *tenantState
	n   int
}

func simBatch(c *simClient) batch {
	b := batch{ReplicaID: c.id, LWW: []lwwEntry{}, Adds: []orAdd{}, Removes: []orRemove{}}
	for k, r := range c.ts.regs.Regs {
		b.LWW = append(b.LWW, lwwEntry{Key: k, Value: r.Value, HLC: hlcOf(r.Stamp), ReplicaID: r.Stamp.Node, Deleted: r.Deleted})
	}
	for name, o := range c.ts.sets {
		d := o.State()
		for el, tags := range d.Adds {
			for _, t := range tags {
				b.Adds = append(b.Adds, orAdd{Set: name, Element: el, Tag: t})
			}
		}
		for _, t := range d.Removed {
			b.Removes = append(b.Removes, orRemove{Set: name, Tag: t})
		}
	}
	sort.Slice(b.LWW, func(i, j int) bool { return b.LWW[i].Key < b.LWW[j].Key })
	return b
}

func simApply(c *simClient, b batch) {
	for _, e := range b.LWW {
		c.ts.regs.Merge(map[string]crdt.Register{e.Key: {Value: e.Value, Stamp: stampOf(e), Deleted: e.Deleted}})
	}
	for _, a := range b.Adds {
		c.ts.set(a.Set, c.id).Merge(crdt.Delta{Adds: map[string][]string{a.Element: {a.Tag}}})
	}
	for _, r := range b.Removes {
		c.ts.set(r.Set, c.id).Merge(crdt.Delta{Removed: []string{r.Tag}})
	}
}

func TestOfflineSyncConvergence(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	srv := NewServer("hub")
	var tick int64 = 1_000
	clients := make([]*simClient, simClients)
	for i := range clients {
		skew := int64(i % 3) // clocks disagree a little, so many edits collide on the same wall time
		id := fmt.Sprintf("c%02d", i)
		clients[i] = &simClient{id: id, hlc: crdt.NewHLC(id, func() int64 { return tick + skew }),
			ts: &tenantState{regs: crdt.NewMap(), sets: map[string]*crdt.ORSet{}}}
	}

	// offline phase: conflicting edits on a small set of shared keys and one shared set
	for e := 0; e < simEdits; e++ {
		if e%5 == 0 {
			tick++
		}
		c := clients[rng.Intn(simClients)]
		c.n++
		key := fmt.Sprintf("incident:%d", rng.Intn(simKeys))
		elem := fmt.Sprintf("unit-%d", rng.Intn(simKeys))
		switch op := rng.Intn(10); {
		case op <= 5:
			c.ts.regs.Set(key, fmt.Sprintf("%s-%d", c.id, c.n), c.hlc.Now())
		case op == 6:
			c.ts.regs.Delete(key, c.hlc.Now())
		case op <= 8:
			c.ts.set("units", c.id).Add(elem)
		default:
			c.ts.set("units", c.id).Remove(elem)
		}
	}
	distinct := map[string]bool{}
	for _, c := range clients {
		h, err := c.ts.hash()
		if err != nil {
			t.Fatal(err)
		}
		distinct[h] = true
	}
	if len(distinct) < 2 {
		t.Fatal("the clients never diverged, so the run proves nothing")
	}

	// reconnect: push and pull through the server, each call lost with probability simLoss
	start := time.Now()
	lost, calls, rounds := 0, 0, 0
	converged := false
	for rounds < 300 && !converged {
		rounds++
		for _, c := range clients {
			calls++
			if rng.Float64() < simLoss {
				lost++
			} else {
				p, _ := json.Marshal(simBatch(c))
				if _, err := srv.PushDelta(ctxFor("t1"), &pb.DeltaBatch{Payload: p}); err != nil {
					t.Fatalf("push: %v", err)
				}
			}
			calls++
			if rng.Float64() < simLoss {
				lost++
			} else {
				out, err := srv.PullDelta(ctxFor("t1"), &pb.PullRequest{})
				if err != nil {
					t.Fatalf("pull: %v", err)
				}
				var b batch
				if err := json.Unmarshal(out.Payload, &b); err != nil {
					t.Fatal(err)
				}
				simApply(c, b)
			}
		}
		sh := hashOf(t, srv, "t1")
		converged = true
		for _, c := range clients {
			if h, _ := c.ts.hash(); h != sh {
				converged = false
				break
			}
		}
	}
	elapsed := time.Since(start)
	if !converged {
		t.Fatalf("clients did not converge in %d rounds", rounds)
	}
	if lost == 0 || float64(lost)/float64(calls) < 0.1 {
		t.Fatalf("the link was not lossy: %d of %d calls lost", lost, calls)
	}
	t.Logf("%d clients, %d edits, %d of %d calls lost, %d rounds", simClients, simEdits, lost, calls, rounds)
	fmt.Printf("BENCH offline_sync_convergence %.3f\n", elapsed.Seconds())
}
