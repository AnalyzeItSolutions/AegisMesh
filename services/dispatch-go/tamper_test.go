package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const (
	tamperChainLen = 100_000
	tamperAt       = 73_421 // 0-based index of the event that gets one character changed
)

// Builds a 100,000-event chain through the real store, flips one character in the middle, and times the full chain walk that finds it.
func TestChainTamperDetect(t *testing.T) {
	st := NewMemoryStore()
	d := NewDispatcher(st, NewLocks())
	for i := 0; i < tamperChainLen; i++ {
		p := json.RawMessage(fmt.Sprintf(`{"edge_id":"edge-%d"}`, i))
		if _, err := st.Append(bg, tenantA, inc1, RoadBlocked, p, actor); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if r, err := d.VerifyChain(bg, tenantA); err != nil || !r.OK || r.Checked != tamperChainLen {
		t.Fatalf("the untouched chain must verify: %+v %v", r, err)
	}

	p := []byte(st.chains[tenantA][tamperAt].Payload)
	p[len(p)-3] ^= 0x01 // one character, one bit: "edge-73421" becomes a neighbouring character
	st.chains[tenantA][tamperAt].Payload = json.RawMessage(p)

	start := time.Now()
	r, err := d.VerifyChain(bg, tenantA)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if r.OK || r.FirstBrokenSequenceID == nil || *r.FirstBrokenSequenceID != int64(tamperAt+1) {
		t.Fatalf("expected the break named at sequence %d, got %+v", tamperAt+1, r)
	}
	t.Logf("tamper at sequence %d found after checking %d of %d events", *r.FirstBrokenSequenceID, r.Checked, tamperChainLen)
	fmt.Printf("BENCH chain_tamper_detect %.3f\n", elapsed.Seconds())
}
