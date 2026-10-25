package api

import (
	"strings"
)

// shellQuote quotes s for a POSIX shell with single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// installCommand is the one-line command shown to the user for enrolling a
// host. It carries everything the host needs to trust the server without
// trusting the network: the pin of the web certificate (so curl refuses a
// look-alike server even though that certificate is not publicly trusted) and
// the fingerprint of the CA behind the agent gateway.
func (a *API) installCommand(token string) string {
	curl := "curl -fsS"
	args := []string{"--server", shellQuote(a.cfg.GatewayURL), "--token", shellQuote(token), "--ca-pin", shellQuote(a.cfg.CAPin)}
	if a.cfg.WebPin != "" {
		// -k only skips the (impossible) public-CA check; --pinnedpubkey is
		// still enforced and is the actual authentication.
		curl += " -k --pinnedpubkey " + shellQuote(a.cfg.WebPin)
		args = append(args, "--web", shellQuote(a.cfg.WebURL), "--web-pin", shellQuote(a.cfg.WebPin))
	} else {
		args = append(args, "--web", shellQuote(a.cfg.WebURL))
	}
	return curl + " " + shellQuote(strings.TrimRight(a.cfg.WebURL, "/")+"/install.sh") + " | sh -s -- " + strings.Join(args, " ")
}
