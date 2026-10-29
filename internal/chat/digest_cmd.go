package chat

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
)

// Shortcuts for the AI digests, sent by a linked account:
//
//	/logs [host name] [1h|6h|24h]   log summary (default: every host, 24h)
//	/security [host name]           security digest (default: every host)
//
// Any other text goes to Ask.

const (
	digestCmdLogs     = "logs"
	digestCmdSecurity = "security"

	replyDigestWindow = "The window must be 1h, 6h or 24h, for example /logs hel1 6h."
	replyDigestHosts  = "The hosts could not be read; try again."
	replyNoHosts      = "There are no hosts yet."
)

// digestOperator is the part of the operator the shortcuts use (the
// operator package's Digester). The chat service asserts it at runtime so
// the OperatorClient interface and its fakes stay unchanged.
type digestOperator interface {
	SummarizeLogs(ctx context.Context, orgID uuid.UUID, scope operator.LogScope) (operator.Digest, error)
	SecurityDigest(ctx context.Context, orgID uuid.UUID, scope operator.SecurityScope) (operator.Digest, error)
}

// digestCommand is a parsed shortcut.
type digestCommand struct {
	kind   string        // digestCmdLogs | digestCmdSecurity
	host   string        // host name as typed; "" means every host
	window time.Duration // logs only; 0 means the operator's default (24h)
}

var durationLike = regexp.MustCompile(`^[0-9]+[hHdD]$`)

// parseDigestCommand reports whether text is one of the digest shortcuts. ok
// is false for any other text. err is set when the shortcut's window is not
// 1h, 6h or 24h. The command word is case-insensitive and may carry a bot
// suffix (/logs@haetae_bot). The window may come before or after the host name.
func parseDigestCommand(text string) (cmd digestCommand, ok bool, err error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return digestCommand{}, false, nil
	}
	word := strings.ToLower(fields[0])
	if i := strings.IndexByte(word, '@'); i > 0 {
		word = word[:i]
	}
	switch word {
	case "/logs":
		cmd.kind = digestCmdLogs
	case "/security":
		cmd.kind = digestCmdSecurity
	default:
		return digestCommand{}, false, nil
	}
	rest := fields[1:]
	if cmd.kind == digestCmdLogs {
		var names []string
		windows := 0
		for _, f := range rest {
			switch strings.ToLower(f) {
			case "1h":
				cmd.window, windows = time.Hour, windows+1
			case "6h":
				cmd.window, windows = 6*time.Hour, windows+1
			case "24h":
				cmd.window, windows = 24*time.Hour, windows+1
			default:
				if durationLike.MatchString(f) {
					return digestCommand{}, true, operator.ErrBadWindow
				}
				names = append(names, f)
			}
		}
		if windows > 1 {
			return digestCommand{}, true, operator.ErrBadWindow
		}
		rest = names
	}
	cmd.host = strings.Join(rest, " ")
	return cmd, true, nil
}

// digestShortcut answers a shortcut. It returns an immediate reply, or "" when
// the digest follows as an answer message from its own goroutine (with the
// same 90 s budget and concurrency cap as Ask).
func (s *Service) digestShortcut(ctx context.Context, acc store.ChatAccount, channel string, cmd digestCommand) string {
	dop, ok := s.operator().(digestOperator)
	if !ok {
		return OperatorDisabledReply
	}
	hostID, reply := s.resolveDigestHost(ctx, acc.OrgID, cmd.host)
	if reply != "" {
		return reply
	}
	select {
	case s.ops.askSem <- struct{}{}:
	default:
		return replyBusy
	}
	go func() {
		defer func() { <-s.ops.askSem }()
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("digest shortcut panic", "panic", r)
			}
		}()
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), askTimeout)
		defer cancel()
		d, err := callTimeout(cctx, func(c context.Context) (operator.Digest, error) {
			if cmd.kind == digestCmdLogs {
				return dop.SummarizeLogs(c, acc.OrgID, operator.LogScope{HostID: hostID, Window: cmd.window})
			}
			return dop.SecurityDigest(c, acc.OrgID, operator.SecurityScope{HostID: hostID})
		})
		if err != nil {
			s.debugLimited("digest shortcut failed", "err", err)
			s.sendToAccount(acc, channel, store.KindAnswer, unavailableReply(reasonFor(err)), nil)
			return
		}
		s.sendToAccount(acc, channel, store.KindAnswer, renderDigest(d), evidenceLinks(d.Evidence))
	}()
	return ""
}

// resolveDigestHost maps a typed host name (case-insensitive) to its id. An
// empty name is every host. An unknown name returns a reply listing the names.
func (s *Service) resolveDigestHost(ctx context.Context, orgID uuid.UUID, name string) (uuid.UUID, string) {
	if name == "" {
		return uuid.Nil, ""
	}
	hosts, err := s.st.ListHosts(ctx, orgID)
	if err != nil {
		s.log.Error("digest shortcut hosts", "err", err)
		return uuid.Nil, replyDigestHosts
	}
	if len(hosts) == 0 {
		return uuid.Nil, replyNoHosts
	}
	names := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if strings.EqualFold(h.Name, name) {
			return h.ID, ""
		}
		names = append(names, LimitRunes(stripControls(h.Name), maxFieldRunes))
	}
	return uuid.Nil, fmt.Sprintf("No host named %q. Hosts: %s.",
		LimitRunes(stripControls(name), maxFieldRunes), strings.Join(names, ", "))
}

// renderDigest is the reply text of a digest (escaping happens when it is
// enqueued). A rule-based digest says why the AI was not used.
func renderDigest(d operator.Digest) string {
	body := d.Text()
	if !d.UsedAI && d.Reason != "" {
		body += "\n\n(rule-based: " + d.Reason + ")"
	}
	return body
}
