package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/api"
	"github.com/alexseymer/haetae/internal/gateway"
	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/operator/provider"
	"github.com/alexseymer/haetae/internal/store"
)

// providerFromEnv builds the AI provider from HAETAE_AI_*. With no
// HAETAE_AI_PROVIDER the AI is off and no AI connection is ever made (S11).
func providerFromEnv() (provider.Provider, error) {
	kind := strings.TrimSpace(os.Getenv("HAETAE_AI_PROVIDER"))
	p, err := provider.New(provider.Config{
		Kind:    kind,
		BaseURL: strings.TrimSpace(os.Getenv("HAETAE_AI_BASE_URL")),
		APIKey:  os.Getenv("HAETAE_AI_API_KEY"),
		Model:   strings.TrimSpace(os.Getenv("HAETAE_AI_MODEL")),
	})
	if err != nil {
		return nil, fmt.Errorf("HAETAE_AI_*: %w", err)
	}
	return p, nil
}

// startOperator builds the operator and installs the hooks that need it that
// live outside the HTTP layer (the log judge). The gateway is both the
// command sender and, for the settings API, the policy pusher.
func startOperator(st *store.Store, gw *gateway.Gateway, log *slog.Logger) (*operator.Service, operatorSettings, error) {
	prov, err := providerFromEnv()
	if err != nil {
		return nil, operatorSettings{}, err
	}
	op := operator.New(operator.Config{
		Store:     st,
		Provider:  prov,
		Commander: gw,
		Log:       log.With("component", "operator"),
	})
	if prov != nil {
		gateway.SetAIJudge(op)
		log.Info("AI operator configured", "provider", prov.Name(), "local", prov.Local(),
			"note", "off for every organisation until an administrator enables it")
	}
	return op, operatorSettings{st: st, op: op, prov: prov}, nil
}

// operatorSettings adapts the store and the operator to api.OperatorSettings.
type operatorSettings struct {
	st   *store.Store
	op   *operator.Service
	prov provider.Provider
}

func (o operatorSettings) GetSettings(ctx context.Context, org uuid.UUID) (api.OperatorSettingsValue, error) {
	s, err := o.st.GetOperatorSettings(ctx, org)
	if err != nil {
		return api.OperatorSettingsValue{}, err
	}
	return api.OperatorSettingsValue{AIEnabled: s.AIEnabled, Autonomy: s.Autonomy, DailyTokenCap: s.DailyTokenCap}, nil
}

func (o operatorSettings) SetSettings(ctx context.Context, org uuid.UUID, v api.OperatorSettingsValue) error {
	return o.st.SetOperatorSettings(ctx, org, store.OperatorSettings{
		AIEnabled: v.AIEnabled, Autonomy: v.Autonomy, DailyTokenCap: v.DailyTokenCap,
	})
}

func (o operatorSettings) UsageToday(ctx context.Context, org uuid.UUID) (api.OperatorUsage, error) {
	u, err := o.st.UsageToday(ctx, org, time.Now())
	if err != nil {
		return api.OperatorUsage{}, err
	}
	return api.OperatorUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}, nil
}

func (o operatorSettings) ProviderInfo() api.OperatorProvider {
	if o.prov == nil {
		return api.OperatorProvider{}
	}
	return api.OperatorProvider{Configured: true, Name: o.prov.Name(), Local: o.prov.Local()}
}

func (o operatorSettings) Available(ctx context.Context, org uuid.UUID) (bool, string) {
	return o.op.Available(ctx, org)
}
