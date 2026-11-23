package crdt

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestHLCNeverGoesBackAndFollowsRemote(t *testing.T) {
	now := int64(1000)
	a := NewHLC("a", func() int64 { return now })
	s1, s2 := a.Now(), a.Now()
	if !s1.Less(s2) || s2.Counter != 1 {
		t.Fatalf("stamps %v %v", s1, s2)
	}
	now = 900
	if !s2.Less(a.Now()) {
		t.Fatal("clock went back")
	}
	b := NewHLC("b", func() int64 { return now })
	r := b.Update(Stamp{5000, 7, "a"})
	if !(Stamp{5000, 7, "a"}).Less(r) || !r.Less(b.Now()) {
		t.Fatal("update not after remote")
	}
	if d, err := DecodeStamp(r.Encode()); err != nil || d != r {
		t.Fatalf("roundtrip %v %v", d, err)
	}
}

func TestLWWSameWinnerEverywhere(t *testing.T) {
	x, y := NewMap(), NewMap()
	s1, s2 := Stamp{10, 0, "a"}, Stamp{10, 0, "b"}
	x.Set("k", "from-a", s1)
	x.Set("k", "from-b", s2)
	y.Set("k", "from-b", s2)
	y.Set("k", "from-a", s1)
	vx, _ := x.Get("k")
	vy, _ := y.Get("k")
	if vx != "from-b" || vy != "from-b" {
		t.Fatalf("%q %q", vx, vy)
	}
	x.Delete("k", Stamp{11, 0, "a"})
	if _, ok := x.Get("k"); ok {
		t.Fatal("delete lost")
	}
}

func TestORSetAddWins(t *testing.T) {
	a, b := NewORSet("a"), NewORSet("b")
	b.Merge(a.Add("x"))
	rm := b.Remove("x")
	again := a.Add("x")
	a.Merge(rm)
	b.Merge(again)
	if !a.Contains("x") || !b.Contains("x") {
		t.Fatal("concurrent add lost")
	}
}

func TestReplicasConvergeToOneHash(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const n = 5
	sets := make([]*ORSet, n)
	maps := make([]*Map, n)
	clocks := make([]*HLC, n)
	for i := range sets {
		i := i
		sets[i], maps[i] = NewORSet(fmt.Sprintf("n%d", i)), NewMap()
		clocks[i] = NewHLC(fmt.Sprintf("n%d", i), func() int64 { return int64(1000 + i) })
	}
	type op struct {
		set bool
		d   Delta
		m   map[string]Register
	}
	var ops []op
	for step := 0; step < 300; step++ {
		i, key := rng.Intn(n), fmt.Sprintf("k%d", rng.Intn(8))
		if rng.Float64() < .7 {
			ops = append(ops, op{set: true, d: sets[i].Add(key)})
		} else {
			ops = append(ops, op{set: true, d: sets[i].Remove(key)})
		}
		ops = append(ops, op{m: maps[i].Set(key, fmt.Sprint(step), clocks[i].Now())})
	}
	hashes := map[string]bool{}
	for i := 0; i < n; i++ {
		order := append([]op{}, ops...)
		for _, k := range rng.Perm(len(ops))[:60] {
			order = append(order, ops[k])
		}
		rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
		for _, o := range order {
			if o.set {
				sets[i].Merge(o.d)
			} else {
				maps[i].Merge(o.m)
			}
		}
		h, err := StateHash(sets[i].State(), maps[i].Regs)
		if err != nil {
			t.Fatal(err)
		}
		hashes[h] = true
	}
	if len(hashes) != 1 {
		t.Fatalf("%d different states", len(hashes))
	}
}
