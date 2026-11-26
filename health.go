package telegram

import (
	"context"
	"errors"
	"net/http"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	reasonTimeout     = "timeout"
	reasonCanceled    = "canceled"
	reasonNetwork     = "network error"
	reasonBadResponse = "invalid response"
	reasonNoIdentity  = "no bot name in response"
	nameArg           = "Name"
	reasonArg         = "Reason"
)

type healthProbe struct {
	base string
}

func newHealthProbe(base string) contracts.HealthProber {
	return healthProbe{base: base}.check
}

func (p healthProbe) check(ctx context.Context, access contracts.ProbeAccess) contracts.ProbeReport {
	token := probeToken(access)
	if token == "" {
		return contracts.ProbeReport{
			State:  contracts.HealthOff,
			Detail: contracts.Notice{Code: contracts.CodeHealthEnvMissing, Args: []contracts.Arg{contracts.Text(nameArg, botVariable)}},
			Remedy: contracts.Notice{Code: contracts.CodeHealthRemedyEnvRestart, Args: []contracts.Arg{contracts.Text(nameArg, botVariable)}},
		}
	}
	start := time.Now()
	identity, err := newBotAPI(p.base, token).getMe(ctx)
	latency := time.Since(start)
	if err != nil {
		return reportFailure(ctx, err, latency)
	}
	if identity.Username == "" {
		return unreachable(reasonNoIdentity, latency)
	}
	return contracts.ProbeReport{
		State:   contracts.HealthOK,
		Latency: latency,
		Detail:  contracts.Notice{Code: contracts.CodeHealthSignedInAs, Args: []contracts.Arg{contracts.Text(nameArg, "@"+identity.Username)}},
	}
}

func probeToken(access contracts.ProbeAccess) string {
	if token := access.Config.Get(tokenSetting); token != "" {
		return token
	}
	if access.Getenv == nil {
		return ""
	}
	return access.Getenv(botVariable)
}

func reportFailure(ctx context.Context, err error, latency time.Duration) contracts.ProbeReport {
	if failure, ok := errors.AsType[*apiError](err); ok {
		switch failure.Code {
		case http.StatusUnauthorized, http.StatusNotFound:
			return contracts.ProbeReport{State: contracts.HealthDown, Latency: latency, Detail: contracts.Notice{Code: contracts.CodeHealthCredentialsRefused}}
		case http.StatusTooManyRequests:
			return contracts.ProbeReport{State: contracts.HealthDegraded, Latency: latency, Detail: contracts.Notice{Code: contracts.CodeHealthRateLimited}}
		}
		return unreachable(failure.Error(), latency)
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return unreachable(reasonTimeout, latency)
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return unreachable(reasonCanceled, latency)
	case errors.Is(err, errBadResponse):
		return unreachable(reasonBadResponse, latency)
	}
	return unreachable(reasonNetwork, latency)
}

func unreachable(reason string, latency time.Duration) contracts.ProbeReport {
	return contracts.ProbeReport{
		State:   contracts.HealthDegraded,
		Latency: latency,
		Detail:  contracts.Notice{Code: contracts.CodeHealthUnreachable, Args: []contracts.Arg{contracts.Text(reasonArg, reason)}},
	}
}
