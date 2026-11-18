package cdp

import (
	"sync"
	"time"
)

// Keeper starts the browser on the first Acquire and closes it once nobody
// has held it for Idle. A browser that died is started again on the next
// Acquire. Safe for concurrent use.
type Keeper struct {
	Start func() (*Browser, error)
	Idle  time.Duration

	mu    sync.Mutex
	b     *Browser
	users int
	timer *time.Timer
}

// Acquire returns the running browser and the release the caller must call
// once, when its job no longer needs it.
func (k *Keeper) Acquire() (*Browser, func(), error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
	if k.b != nil && k.b.Err() != nil {
		k.b, k.users = nil, 0 // the dead one's holders release into nothing
	}
	if k.b == nil {
		b, err := k.Start()
		if err != nil {
			return nil, nil, err
		}
		k.b = b
	}
	k.users++
	b := k.b
	var once sync.Once
	return b, func() { once.Do(func() { k.release(b) }) }, nil
}

func (k *Keeper) release(b *Browser) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.b != b { // released after it died and was replaced
		return
	}
	k.users--
	if k.users > 0 {
		return
	}
	k.timer = time.AfterFunc(k.Idle, func() {
		k.mu.Lock()
		if k.b != b || k.users > 0 {
			k.mu.Unlock()
			return
		}
		k.b, k.timer = nil, nil
		k.mu.Unlock()
		b.Close()
	})
}

// Close closes the browser now, held or not.
func (k *Keeper) Close() {
	k.mu.Lock()
	b := k.b
	k.b, k.users = nil, 0
	if k.timer != nil {
		k.timer.Stop()
		k.timer = nil
	}
	k.mu.Unlock()
	if b != nil {
		b.Close()
	}
}
