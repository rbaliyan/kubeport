// Package config re-exports pkg/config for internal use.
// All types, functions, and errors are defined in pkg/config.
// This shim exists so that existing internal packages can continue
// to import "internal/config" without modification.
package config

import pkgconfig "github.com/rbaliyan/kubeport/pkg/config"

// Type aliases — all internal code continues to use config.Config, etc.
type Config = pkgconfig.Config
type ServiceConfig = pkgconfig.ServiceConfig
type PortSelector = pkgconfig.PortSelector
type PortsConfig = pkgconfig.PortsConfig
type ListenMode = pkgconfig.ListenMode
type ListenConfig = pkgconfig.ListenConfig
type Format = pkgconfig.Format
type HookConfig = pkgconfig.HookConfig
type WebhookConfig = pkgconfig.WebhookConfig
type ExecConfig = pkgconfig.ExecConfig
type SupervisorConfig = pkgconfig.SupervisorConfig
type ParsedSupervisorConfig = pkgconfig.ParsedSupervisorConfig

// Re-export constants.
const (
	ListenUnix = pkgconfig.ListenUnix
	ListenTCP  = pkgconfig.ListenTCP
	FormatYAML = pkgconfig.FormatYAML
	FormatTOML = pkgconfig.FormatTOML
)

// Re-export sentinel errors.
var (
	ErrNoConfig        = pkgconfig.ErrNoConfig
	ErrNoServices      = pkgconfig.ErrNoServices
	ErrServiceExists   = pkgconfig.ErrServiceExists
	ErrServiceNotFound = pkgconfig.ErrServiceNotFound
	ErrConfigExists    = pkgconfig.ErrConfigExists
)

// Re-export functions.
var (
	Load            = pkgconfig.Load
	LoadForEdit     = pkgconfig.LoadForEdit
	LoadServices    = pkgconfig.LoadServices
	Init            = pkgconfig.Init
	Discover        = pkgconfig.Discover
	NewInMemory     = pkgconfig.NewInMemory
	ValidateService = pkgconfig.ValidateService
)
