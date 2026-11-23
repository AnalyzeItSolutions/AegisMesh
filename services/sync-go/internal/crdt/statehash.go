package crdt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// StateHash is one SHA-256 over any number of states; encoding/json writes map keys sorted, so equal states give byte-identical hashes.
func StateHash(states ...any) (string, error) {
	b, err := json.Marshal(states)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
