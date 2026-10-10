//go:build linux

// The aliases below expose the stable public facade. The implementation lives
// under internal/app; these aliases keep the import path and API stable.
package encryl

import "github.com/encryptedbusiness/encryl/internal/app"

type (
	Server             = app.Server
	Config             = app.Config
	ConfigError        = app.ConfigError
	ConfigProblem      = app.ConfigProblem
	BackpressurePolicy = app.BackpressurePolicy
	Conn               = app.Conn
	ConnID             = app.ConnID
	CloseInfo          = app.CloseInfo
	Emitter            = app.Emitter
	Topic              = app.Topic
	TopicTarget        = app.TopicTarget
	Stats              = app.Stats
	Message            = app.Message
	Ack                = app.Ack
	Handler            = app.Handler
	HandlerFunc        = app.HandlerFunc
	EventHandler       = app.EventHandler
	RequestHandler     = app.RequestHandler
	ConnectHandler     = app.ConnectHandler
	ConnectMiddleware  = app.ConnectMiddleware
	DisconnectHandler  = app.DisconnectHandler
	EventMiddleware    = app.EventMiddleware
	UpgradeError       = app.UpgradeError
	UpgradeHeaders     = app.UpgradeHeaders
	UpgradeSpec        = app.UpgradeSpec
)

const (
	BackpressureReject     = app.BackpressureReject
	BackpressureDropNewest = app.BackpressureDropNewest
	BackpressureDropOldest = app.BackpressureDropOldest
	BackpressureCloseSlow  = app.BackpressureCloseSlow
)

var (
	ErrClosed          = app.ErrClosed
	ErrBackpressure    = app.ErrBackpressure
	ErrSlowConsumer    = app.ErrSlowConsumer
	ErrPayloadTooLarge = app.ErrPayloadTooLarge
	ErrUnauthorized    = app.ErrUnauthorized
	ErrRoomLimit       = app.ErrRoomLimit
	ErrUnsupported     = app.ErrUnsupported
	ErrAckTimeout      = app.ErrAckTimeout
	ErrInvalidUpgrade  = app.ErrInvalidUpgrade
)

// New creates a server from a validated configuration.
func New(cfg Config) (*Server, error) { return app.New(cfg) }

// DefaultConfig returns the production defaults.
func DefaultConfig() Config { return app.DefaultConfig() }

// EventIDForName returns the stable FNV-1a ID for an event name.
func EventIDForName(name string) uint32 { return app.EventIDForName(name) }
