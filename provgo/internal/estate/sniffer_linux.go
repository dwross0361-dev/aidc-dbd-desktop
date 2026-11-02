//go:build linux

package estate

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// Sniffer reads DHCP DISCOVER/REQUEST frames on the provisioning NIC and hands them to Evidence.
func Sniffer(e *Estate, nic string) {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(0x0800)))
	if err == nil && nic != "" {
		ifc, ierr := net.InterfaceByName(nic)
		if ierr != nil {
			err = ierr
		} else {
			err = syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(0x0800), Ifindex: ifc.Index})
		}
	}
	if err != nil {
		if fd > 0 {
			syscall.Close(fd)
		}
		e.Event(fmt.Sprintf("DHCP sniffer off (%s): vendor-class serials come only from Kea's lease hook", pyErrno(err)), "warn")
		return
	}
	if nic == "" {
		e.Event("DHCP sniffer listens on every interface: set prov_nic in estate.conf (setup.sh does)", "warn")
	}
	buf := make([]byte, 4096)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sniffer: %v\n", err)
			time.Sleep(time.Second)
			continue
		}
		func() {
			defer func() {
				if x := recover(); x != nil {
					fmt.Fprintf(os.Stderr, "sniffer: %v\n", x)
				}
			}()
			ev := ParseDHCP(append([]byte(nil), buf[:n]...))
			if ev != nil {
				if t := ev.Int("type"); ev.Has("type") && (t == 1 || t == 3) {
					e.Evidence(ev, "sniffer")
				}
			}
		}()
	}
}
