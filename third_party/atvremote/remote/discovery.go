package remote

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

// MDNSServiceType is the mDNS service type advertised by Android TVs
// supporting the v2 remote protocol.
const MDNSServiceType = "_androidtvremote2._tcp"

// DefaultDiscoveryTimeout is how long Discover browses the network when no
// timeout is given.
const DefaultDiscoveryTimeout = 3 * time.Second

// DiscoveredDevice is an Android TV found on the local network via mDNS.
type DiscoveredDevice struct {
	// Name is the mDNS instance name, e.g. "Living Room TV".
	Name string `json:"name"`
	// Host is the advertised hostname, e.g. "Chromecast.local.".
	Host string `json:"host"`
	// IP is the device's IP address, to be passed to New.
	IP string `json:"ip"`
	// Port is the advertised api port, normally 6466.
	Port int `json:"port"`
}

// Discover browses the local network via mDNS for Android TVs supporting the
// v2 remote protocol, waiting at most timeout (0 uses
// DefaultDiscoveryTimeout) before returning the devices found.
func Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	if timeout <= 0 {
		timeout = DefaultDiscoveryTimeout
	}

	entries := make(chan *mdns.ServiceEntry, 32)
	done := make(chan []DiscoveredDevice)
	go func() {
		var devices []DiscoveredDevice
		seen := map[string]bool{}
		for entry := range entries {
			// The mdns package can surface unrelated services that answer
			// during the query window; keep only the requested service type.
			if !strings.Contains(entry.Name, MDNSServiceType+".") {
				continue
			}
			ip := entry.AddrV4
			if ip == nil {
				ip = entry.AddrV6
			}
			if ip == nil || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			devices = append(devices, DiscoveredDevice{
				Name: instanceName(entry.Name),
				Host: entry.Host,
				IP:   ip.String(),
				Port: entry.Port,
			})
		}
		done <- devices
	}()

	params := mdns.DefaultParams(MDNSServiceType)
	params.Entries = entries
	params.Timeout = timeout
	// The mdns package logs harmless warnings (e.g. missing IPv6 support) to
	// the standard logger; keep them out of normal output.
	params.Logger = log.New(discardWriter{}, "", 0)
	err := mdns.QueryContext(ctx, params)
	close(entries)
	devices := <-done
	if err != nil {
		return nil, fmt.Errorf("mdns query: %w", err)
	}
	return devices, nil
}

// instanceName extracts the unescaped instance name from a full mDNS entry
// name like "Living\ Room\ TV._androidtvremote2._tcp.local.".
func instanceName(entryName string) string {
	name := strings.TrimSuffix(entryName, ".")
	if i := strings.Index(name, "."+MDNSServiceType); i >= 0 {
		name = name[:i]
	}
	return unescapeDNS(name)
}

// unescapeDNS reverses DNS name escaping: `\X` for special characters and
// `\DDD` decimal escapes for arbitrary bytes.
func unescapeDNS(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		if i+2 < len(s) && isDigit(s[i]) && isDigit(s[i+1]) && isDigit(s[i+2]) {
			n, err := strconv.Atoi(s[i : i+3])
			if err == nil && n <= 255 {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// discardWriter drops all log output.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
