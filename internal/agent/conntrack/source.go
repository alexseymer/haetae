// Package conntrack attributes outbound connections from Docker containers
// using conntrack NEW events. The ti-mo/conntrack library is confined to this
// package behind EventSource so tests can drive a fake source (this VM has no
// nf_conntrack).
package conntrack

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	ct "github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"
	"golang.org/x/sys/unix"
)

// Event is one NEW connection seen by the listener.
type Event struct {
	Proto       string // tcp, udp
	OrigSrc     netip.Addr
	OrigDst     netip.Addr
	OrigDstPort uint16
}

// EventSource produces NEW connection events until ctx ends or the source fails.
type EventSource interface {
	// Run delivers events to emit until ctx is done or a fatal error occurs.
	// It returns the terminal error (nil on clean cancel).
	Run(ctx context.Context, emit func(Event)) error
}

// KernelSource listens via ti-mo/conntrack netlink multicast.
type KernelSource struct{}

// Available reports whether nf_conntrack appears present on this host.
// /proc/net/nf_conntrack exists only with the deprecated
// CONFIG_NF_CONNTRACK_PROCFS, which many current kernels leave out, so the
// module's sysctl directory is checked too; the netlink dial in Run decides.
func Available() (bool, string) {
	return available("/proc")
}

func available(procRoot string) (bool, string) {
	var firstErr error
	for _, p := range []string{"sys/net/netfilter/nf_conntrack_max", "net/nf_conntrack"} {
		_, err := os.Stat(filepath.Join(procRoot, p))
		if err == nil {
			return true, ""
		}
		if !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return false, firstErr.Error()
	}
	return false, "no nf_conntrack"
}

// Run opens a netlink conntrack listener. Permission denied and missing
// nf_conntrack become unavailable-class errors; ENOBUFS is returned so the
// Listener can mark degraded and restart.
func (KernelSource) Run(ctx context.Context, emit func(Event)) error {
	if ok, reason := Available(); !ok {
		return unavailableError{reason}
	}
	c, err := ct.Dial(nil)
	if err != nil {
		return classifyDial(err)
	}
	defer c.Close() //nolint:errcheck

	evCh := make(chan ct.Event, 1024)
	errCh, err := c.Listen(evCh, 1, []netfilter.NetlinkGroup{netfilter.GroupCTNew})
	if err != nil {
		return classifyDial(err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-errCh:
			if !ok {
				return errors.New("conntrack: error channel closed")
			}
			if err == nil {
				continue
			}
			if isENOBUFS(err) {
				return degradedError{fmt.Sprintf("netlink overflow: %v", err)}
			}
			return err
		case ev, ok := <-evCh:
			if !ok {
				return errors.New("conntrack: event channel closed")
			}
			if ev.Type != ct.EventNew || ev.Flow == nil {
				continue
			}
			if e, ok := mapFlow(ev.Flow); ok {
				emit(e)
			}
		}
	}
}

func mapFlow(f *ct.Flow) (Event, bool) {
	proto := protoName(f.TupleOrig.Proto.Protocol)
	if proto == "" {
		return Event{}, false
	}
	src := f.TupleOrig.IP.SourceAddress
	dst := f.TupleOrig.IP.DestinationAddress
	if !src.IsValid() || !dst.IsValid() {
		return Event{}, false
	}
	return Event{
		Proto:       proto,
		OrigSrc:     src,
		OrigDst:     dst,
		OrigDstPort: f.TupleOrig.Proto.DestinationPort,
	}, true
}

func protoName(p uint8) string {
	switch p {
	case unix.IPPROTO_TCP:
		return "tcp"
	case unix.IPPROTO_UDP:
		return "udp"
	default:
		return ""
	}
}

type unavailableError struct{ reason string }

func (e unavailableError) Error() string { return e.reason }

func (e unavailableError) Unavailable() bool { return true }

type degradedError struct{ reason string }

func (e degradedError) Error() string { return e.reason }

func (e degradedError) Degraded() bool { return true }

func classifyDial(err error) error {
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "permission denied") || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		return unavailableError{"permission denied"}
	}
	if strings.Contains(low, "no such file") || strings.Contains(low, "nf_conntrack") {
		return unavailableError{msg}
	}
	return err
}

func isENOBUFS(err error) bool {
	return errors.Is(err, unix.ENOBUFS) || strings.Contains(strings.ToLower(err.Error()), "no buffer space")
}

// FakeSource is an EventSource driven by a channel (tests).
type FakeSource struct {
	Events <-chan Event
	Errs   <-chan error
}

// Run forwards Events until ctx ends, Events closes, or an error arrives.
func (f FakeSource) Run(ctx context.Context, emit func(Event)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-f.Errs:
			if !ok {
				f.Errs = nil
				continue
			}
			if err != nil {
				return err
			}
		case ev, ok := <-f.Events:
			if !ok {
				// Idle until ctx ends (listener may restart).
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(time.Hour):
					return nil
				}
			}
			emit(ev)
		}
	}
}
