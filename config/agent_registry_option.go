package config

import "github.com/DotNetAge/goreact/logging"

type agentRegistryOption struct {
	logger logging.Logger
}

type AgentRegistryOption func(*agentRegistryOption)

func WithRegistryLogger(logger logging.Logger) AgentRegistryOption {
	return func(o *agentRegistryOption) { o.logger = logger }
}
