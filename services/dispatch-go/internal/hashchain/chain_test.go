package hashchain

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func build(n int) []Entry {
	var c []Entry
	for i := 0; i < n; i++ {
		c = append(c, Append(c, fmt.Sprintf("agg%d", i%5), "Thing", json.RawMessage(fmt.Sprintf(`{"n":%d,"note":"xxxxxxxxxxxxxxxxxxxx"}`, i)), "actor"))
	}
	return c
}

func TestIntactChainVerifies(t *testing.T) {
	if !Verify(build(50)) || FirstBreak(nil) != -1 {
		t.Fatal("intact chain rejected")
	}
}

func TestOneCharacterChangeIsFound(t *testing.T) {
	c := build(200)
	c[117].Payload = json.RawMessage(`{"n":117,"note":"xxxxxxxxxxxxxxxxxxxy"}`)
	if got := FirstBreak(c); got != 117 {
		t.Fatalf("got %d", got)
	}
}

func TestRemovedEntryIsFound(t *testing.T) {
	c := build(30)
	if got := FirstBreak(append(append([]Entry{}, c[:10]...), c[11:]...)); got != 10 {
		t.Fatalf("got %d", got)
	}
}

func TestLongChainWalkIsFast(t *testing.T) {
	c := build(100000)
	c[77777].ActorID = "mallory"
	start := time.Now()
	if got := FirstBreak(c); got != 77777 {
		t.Fatalf("got %d", got)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("walk took %v", d)
	}
}
