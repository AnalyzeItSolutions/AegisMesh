package hashchain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const Genesis = "0000000000000000000000000000000000000000000000000000000000000000"

// Entry is one event in the chain. EntryHash covers every other field, PrevHash links to the entry before.
type Entry struct {
	SequenceID  int64           `json:"sequence_id"`
	AggregateID string          `json:"aggregate_id"`
	EventType   string          `json:"event_type"`
	Payload     json.RawMessage `json:"payload"`
	ActorID     string          `json:"actor_id"`
	PrevHash    string          `json:"prev_hash"`
	EntryHash   string          `json:"entry_hash"`
}

func (e Entry) Hash() string {
	body, _ := json.Marshal(struct {
		SequenceID  int64           `json:"sequence_id"`
		AggregateID string          `json:"aggregate_id"`
		EventType   string          `json:"event_type"`
		Payload     json.RawMessage `json:"payload"`
		ActorID     string          `json:"actor_id"`
		PrevHash    string          `json:"prev_hash"`
	}{e.SequenceID, e.AggregateID, e.EventType, e.Payload, e.ActorID, e.PrevHash})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Append returns the next entry after chain (which may be empty).
func Append(chain []Entry, aggregateID, eventType string, payload json.RawMessage, actorID string) Entry {
	e := Entry{SequenceID: 1, AggregateID: aggregateID, EventType: eventType, Payload: payload, ActorID: actorID, PrevHash: Genesis}
	if n := len(chain); n > 0 {
		e.SequenceID, e.PrevHash = chain[n-1].SequenceID+1, chain[n-1].EntryHash
	}
	e.EntryHash = e.Hash()
	return e
}

// FirstBreak is the index of the first entry that does not belong (wrong hash, link or sequence), or -1 when the chain is intact.
func FirstBreak(chain []Entry) int {
	prev := Genesis
	for i, e := range chain {
		if e.PrevHash != prev || e.SequenceID != int64(i+1) || e.Hash() != e.EntryHash {
			return i
		}
		prev = e.EntryHash
	}
	return -1
}

func Verify(chain []Entry) bool { return FirstBreak(chain) == -1 }
