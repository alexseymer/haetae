package main

import "golang.org/x/sys/unix"

// dropAmbientCaps clears the ambient capability set. The systemd unit grants
// CAP_NET_ADMIN as an ambient capability so the agent can open the conntrack
// netlink socket; clearing the ambient set keeps it in the agent's own
// permitted and effective sets but stops child processes (the inventory
// helper, which parses untrusted images) from inheriting it.
func dropAmbientCaps() error {
	return unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0)
}
