package chat

import (
	"context"
	"sync"
	"time"

	"github.com/alexseymer/haetae/internal/store"
)

// Run starts the delivery loop and each channel's inbound Run until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for name, ch := range s.channels {
		wg.Add(1)
		go func(name string, ch Channel) {
			defer wg.Done()
			if err := ch.Run(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("chat channel stopped", "channel", name, "err", err)
			}
		}(name, ch)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.deliveryLoop(ctx)
	}()
	wg.Wait()
	return ctx.Err()
}

func (s *Service) deliveryLoop(ctx context.Context) {
	notify := make(chan struct{}, 1)
	go s.listenNotify(ctx, notify)

	ticker := time.NewTicker(s.pollEvery)
	defer ticker.Stop()

	for {
		s.deliverOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-notify:
		case <-s.wake:
		}
	}
}

func (s *Service) listenNotify(ctx context.Context, notify chan<- struct{}) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.listenOnce(ctx, notify); err != nil && ctx.Err() == nil {
			s.log.Warn("chat listen", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (s *Service) listenOnce(ctx context.Context, notify chan<- struct{}) error {
	pool := s.st.Pool()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+store.ChatNotifyChannel); err != nil {
		return err
	}
	for {
		nctx, cancel := context.WithTimeout(ctx, s.pollEvery)
		notification, err := conn.Conn().WaitForNotification(nctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if nctx.Err() == context.DeadlineExceeded {
				continue
			}
			return err
		}
		if notification != nil {
			select {
			case notify <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Service) deliverOnce(ctx context.Context) {
	msgs, err := s.st.ClaimDueChatMessages(ctx, s.claimLimit)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("claim chat messages", "err", err)
		}
		return
	}
	if len(msgs) == 0 {
		return
	}

	byChan := make(map[string][]store.ChatMessage)
	for _, m := range msgs {
		byChan[m.Channel] = append(byChan[m.Channel], m)
	}

	var wg sync.WaitGroup
	for channel, batch := range byChan {
		wg.Add(1)
		go func(channel string, batch []store.ChatMessage) {
			defer wg.Done()
			s.deliverChannel(ctx, channel, batch)
		}(channel, batch)
	}
	wg.Wait()
}

func (s *Service) deliverChannel(ctx context.Context, channel string, batch []store.ChatMessage) {
	ch := s.channels[channel]
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, m := range batch {
		wg.Add(1)
		sem <- struct{}{}
		go func(m store.ChatMessage) {
			defer wg.Done()
			defer func() { <-sem }()
			s.deliverOne(ctx, ch, m)
		}(m)
	}
	wg.Wait()
}

func (s *Service) deliverOne(ctx context.Context, ch Channel, m store.ChatMessage) {
	now := s.now()
	if m.Channel == store.ChannelWeb {
		if err := s.st.MarkDelivered(ctx, m.ID, now); err != nil {
			s.log.Error("mark web delivered", "id", m.ID, "err", err)
		}
		return
	}
	if ch == nil {
		_ = s.st.MarkFailed(ctx, m.ID, store.MaxDeliveryAttempts, "channel not configured", now)
		return
	}

	var account *store.ChatAccount
	if m.AccountID != nil {
		a, err := s.st.GetChatAccount(ctx, m.OrgID, *m.AccountID)
		if err != nil {
			_ = s.st.MarkFailed(ctx, m.ID, m.Attempts+1, err.Error(), now)
			return
		}
		account = &a
	}

	msg := m
	if len(msg.Actions) > 0 && len(s.secret) > 0 {
		exp := now.Add(store.ActionTokenTTL)
		actions := make([]store.ChatAction, len(msg.Actions))
		for i, a := range msg.Actions {
			actions[i] = store.ChatAction{
				ID:    store.SignActionToken(s.secret, m.ID, a.ID, exp),
				Label: a.Label,
			}
		}
		msg.Actions = actions
	}

	if err := ch.Send(ctx, msg, account); err != nil {
		_ = s.st.MarkFailed(ctx, m.ID, m.Attempts+1, err.Error(), now)
		s.log.Warn("chat delivery failed", "channel", m.Channel, "id", m.ID, "attempt", m.Attempts+1, "err", err)
		return
	}
	if err := s.st.MarkDelivered(ctx, m.ID, now); err != nil {
		s.log.Error("mark delivered", "id", m.ID, "err", err)
	}
}

// DeliverOnceForTest runs one delivery pass (tests).
func (s *Service) DeliverOnceForTest(ctx context.Context) {
	s.deliverOnce(ctx)
}
