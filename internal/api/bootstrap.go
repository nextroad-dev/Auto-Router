package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
)

// bootstrapTokenBytes is the entropy of the first-run setup token.
const bootstrapTokenBytes = 16

// BootstrapToken is the one-time credential that authorizes first-run password
// setup from a non-local client. Before an owner password exists the setup
// endpoint has no other credential to check, so without it the first network
// client to reach an exposed instance would become its owner.
//
// The composition root creates it at startup when no password is set and prints
// it to the process log, which only the operator can read. It is consumed by a
// successful setup and never persisted.
type BootstrapToken struct {
	mutex  sync.Mutex
	digest [sha256.Size]byte
	active bool
}

// NewBootstrapToken generates a fresh token and returns it with its value.
func NewBootstrapToken() (*BootstrapToken, string, error) {
	raw := make([]byte, bootstrapTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	value := hex.EncodeToString(raw)
	return &BootstrapToken{digest: sha256.Sum256([]byte(value)), active: true}, value, nil
}

// Verify reports whether presented matches an unconsumed token.
func (b *BootstrapToken) Verify(presented string) bool {
	if b == nil || presented == "" {
		return false
	}
	digest := sha256.Sum256([]byte(presented))
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.active && subtle.ConstantTimeCompare(digest[:], b.digest[:]) == 1
}

// Consume invalidates the token.
func (b *BootstrapToken) Consume() {
	if b == nil {
		return
	}
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.active = false
}
