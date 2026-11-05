package vpn

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"downloadmanagersquared/engine/internal/atomicfile"
	"downloadmanagersquared/engine/internal/netif"
	"downloadmanagersquared/engine/internal/tunnel"
)

// imported is the provider of tunnels whose WireGuard .conf the user imported.
// The configs live in the engine's folder, never in the settings the extension
// gets, since they hold private keys. Ports come by NAT-PMP, or the user types
// one in, kept beside the config.
type imported struct {
	t         *Tunnels
	gatewayOK func(netip.Addr) bool // where NAT-PMP may be sent
	ports     map[string]int        // typed in, by interface ID; under t.mu
}

// options are what is kept beside an imported config: <hex id>.json.
type options struct {
	ForwardedPort int `json:"forwardedPort,omitempty"` // typed in by the user, 0 = none
}

func (*imported) name() string { return "conf" }

func (p *imported) typedPort(te *entry) int { return p.ports[te.id] }

// ready: a config without DNS (saved before that was required) can't run; its error says so.
func (*imported) ready(te *entry) bool { return len(te.cfg.DNS) > 0 }

func (*imported) lostHint() string {
	return ". If this keeps happening, the config may have expired or been revoked at your provider: make a new one and import it again"
}

func (p *imported) config(_ context.Context, te *entry, _ *netif.Binding) (tunnel.Config, func(*tunnel.Tunnel) forwarder, error) {
	p.t.mu.Lock()
	cfg := te.cfg
	p.t.mu.Unlock()
	return cfg, func(tn *tunnel.Tunnel) forwarder { return p.newPMP(te.id, tn, cfg) }, nil
}

func (p *imported) file(ifaceID string) string {
	return filepath.Join(p.t.dir, "tunnels", hex.EncodeToString([]byte(ifaceID))+".conf")
}

func (p *imported) optionsFile(ifaceID string) string {
	return strings.TrimSuffix(p.file(ifaceID), ".conf") + ".json"
}

// loadOptions reads what is kept beside an interface's config. Unreadable
// options are set aside: the tunnel runs without them.
func (p *imported) loadOptions(ifaceID string) {
	f := p.optionsFile(ifaceID)
	b, err := os.ReadFile(f)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var o options
	if err == nil {
		err = json.Unmarshal(b, &o)
	}
	if err == nil && (o.ForwardedPort < 0 || o.ForwardedPort > 65535) {
		err = fmt.Errorf("forwarded port %d", o.ForwardedPort)
	}
	if err != nil {
		log.Printf("tunnel options for %s unreadable, set aside: %v", ifaceID, err)
		setAside(f)
		return
	}
	p.t.mu.Lock()
	p.setPortLocked(ifaceID, o.ForwardedPort)
	p.t.mu.Unlock()
}

func (p *imported) setPortLocked(ifaceID string, port int) {
	if port == 0 {
		delete(p.ports, ifaceID)
	} else {
		p.ports[ifaceID] = port
	}
}

// SetForwardedPort records the port a VPN provider forwards to an interface's
// imported tunnel (0 clears it). It is kept with the tunnel's config.
func (t *Tunnels) SetForwardedPort(ifaceID string, port int) error {
	if port < 0 || port > 65535 {
		return errors.New("a port is a number from 1 to 65535")
	}
	p := t.imported
	t.mu.Lock()
	te := t.tunnels[ifaceID]
	t.mu.Unlock()
	switch {
	case te == nil:
		return errors.New("this connection has no imported VPN tunnel")
	case te.prov != provider(p):
		return errors.New("PIA forwards its own port")
	}
	var err error
	if port == 0 {
		if err = os.Remove(p.optionsFile(ifaceID)); errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		b, _ := json.Marshal(options{ForwardedPort: port})
		err = atomicfile.Write(p.optionsFile(ifaceID), b, 0o600)
	}
	if err != nil {
		return err
	}
	t.mu.Lock()
	p.setPortLocked(ifaceID, port)
	t.mu.Unlock()
	return nil
}

// AdoptForwardedPort gives an imported tunnel a port typed in before ports
// were kept with their tunnels, unless it has one already. A port for an
// interface without an imported tunnel is dropped.
func (t *Tunnels) AdoptForwardedPort(ifaceID string, port int) {
	t.mu.Lock()
	te := t.tunnels[ifaceID]
	has := t.imported.ports[ifaceID] != 0
	t.mu.Unlock()
	if te == nil || te.prov != provider(t.imported) || has {
		return
	}
	if err := t.SetForwardedPort(ifaceID, port); err != nil {
		log.Printf("forwarded port %d for %s: %v", port, ifaceID, err)
	}
}

// load reads the saved configs. One that can't be read is set aside.
func (p *imported) load() []Problem {
	var problems []Problem
	files, _ := filepath.Glob(filepath.Join(p.t.dir, "tunnels", "*.conf"))
	for _, f := range files {
		id, err := hex.DecodeString(strings.TrimSuffix(filepath.Base(f), ".conf"))
		if err != nil {
			continue
		}
		b, err := os.ReadFile(f)
		if err == nil {
			_, err = tunnel.ParseConf(string(b))
		}
		if err != nil && !errors.Is(err, tunnel.ErrNoDNS) { // that one shows on the tunnel
			log.Printf("tunnel for %s unreadable, set aside: %v", id, err)
			setAside(f)
			problems = append(problems, Problem{Iface: string(id), Err: err})
			continue
		}
		if err := p.set(string(id), string(b), false); err != nil {
			log.Printf("tunnel for %s: %v", id, err)
			continue
		}
		p.loadOptions(string(id))
	}
	return problems
}

// set makes conf the tunnel of an interface, replacing any it had. A saved
// config without DNS (from before that was required) is kept, its tunnel down
// saying why.
func (p *imported) set(ifaceID, conf string, save bool) error {
	cfg, err := tunnel.ParseConf(conf)
	var cantRun string
	if errors.Is(err, tunnel.ErrNoDNS) && !save {
		cantRun, err = err.Error(), nil
	}
	if err != nil {
		return err
	}
	if save {
		if err := atomicfile.Write(p.file(ifaceID), []byte(conf), 0o600); err != nil {
			return err
		}
	}
	return p.t.addEntry(ifaceID, p, cfg, cantRun)
}

// withKey is the name of another interface whose imported config has the same
// private key as conf ("" if none, or conf doesn't parse).
func (p *imported) withKey(ifaceID, conf string) string {
	cfg, err := tunnel.ParseConf(conf)
	if err != nil {
		return ""
	}
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	for id, te := range p.t.tunnels {
		if id == ifaceID || te.prov != provider(p) || te.cfg.PrivateKey != cfg.PrivateKey {
			continue
		}
		if i := p.t.ifaces[id]; i.Name != "" {
			return i.Name
		}
		return "Another connection"
	}
	return ""
}

// Import makes conf the tunnel of an interface, replacing any it had, and
// starts it. A config is refused if it isn't a usable WireGuard one, or if
// another interface's imported tunnel has its key.
func (t *Tunnels) Import(ifaceID, conf string) error {
	if other := t.imported.withKey(ifaceID, conf); other != "" {
		return fmt.Errorf("%s already uses this config. Each connection needs its own: the VPN server "+
			"keeps one location per key, so two tunnels with one key take each other's traffic. "+
			"Have your VPN provider make another config (a separate device or key), ideally on another server", other)
	}
	if err := t.imported.set(ifaceID, conf, true); err != nil {
		return fmt.Errorf("not a usable WireGuard config: %w", err)
	}
	t.sync(time.Now())
	return nil
}

// Remove takes an interface's imported tunnel down and deletes its config,
// with the port typed in for it.
func (t *Tunnels) Remove(ifaceID string) error {
	t.removeEntry(ifaceID)
	t.mu.Lock()
	t.imported.setPortLocked(ifaceID, 0)
	t.mu.Unlock()
	for _, f := range []string{t.imported.file(ifaceID), t.imported.optionsFile(ifaceID)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
