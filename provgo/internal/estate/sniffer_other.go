//go:build !linux

package estate

// Sniffer is Linux only (AF_PACKET).
func Sniffer(e *Estate, nic string) {
	e.Event("DHCP sniffer off (not supported on this OS): vendor-class serials come only from Kea's lease hook", "warn")
}
