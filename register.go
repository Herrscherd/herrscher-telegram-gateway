package telegram

import (
	"context"
	"fmt"
	"log/slog"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/tgctl"
)

const telegramAPIBase = "https://api.telegram.org"

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: contracts.Manifest{
			Kind:         gatewayKind,
			Category:     contracts.CategoryGateway,
			Status:       contracts.StatusExperimental,
			Capabilities: contracts.Capabilities{Reactions: true, SelectMenus: true, Replies: true},
			Config:       settingSpecs(),
		},
		Gateway: func(ctx context.Context, cfg contracts.PluginConfig) (contracts.GatewaySet, error) {
			return NewGatewaySet(ctx, cfg, slog.Default())
		},
		Catalog: localeRoot{},
		Probe:   newHealthProbe(telegramAPIBase),
	})
}

// NewGatewaySet builds the Telegram channel from config. It refuses a missing
// token and a bot nobody is allowed to talk to, and asks Telegram who the bot is
// before it returns, so a wrong token stops the start.
func NewGatewaySet(ctx context.Context, cfg contracts.PluginConfig, log *slog.Logger) (contracts.GatewaySet, error) {
	return newGatewaySet(ctx, cfg, telegramAPIBase, log)
}

func newGatewaySet(ctx context.Context, cfg contracts.PluginConfig, base string, log *slog.Logger) (contracts.GatewaySet, error) {
	log = log.With("gateway", gatewayKind)
	set, err := parseSettings(cfg)
	if err != nil {
		return contracts.GatewaySet{}, err
	}
	msgs, err := operatorMessages(log)
	if err != nil {
		return contracts.GatewaySet{}, err
	}
	api := tgctl.New(set.token, tgctl.WithBaseURL(base))
	self, err := api.GetMe(ctx)
	if err != nil {
		return contracts.GatewaySet{}, fmt.Errorf("telegram gateway: resolve the bot identity: %w", err)
	}
	gateway := newGateway(ctx, api, set, self, msgs, log, dialer(base))
	return contracts.GatewaySet{Gateway: gateway, Prober: gateway}, nil
}

func dialer(base string) func(token string) bot {
	return func(token string) bot { return tgctl.New(token, tgctl.WithBaseURL(base)) }
}
