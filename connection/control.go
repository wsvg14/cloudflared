package connection

import (
	"context"
	"io"
	"time"

	"github.com/rs/zerolog"

	tunnelpogs "github.com/cloudflare/cloudflared/tunnelrpc/pogs"
)

// RPCClientFunc derives a named tunnel rpc client that can then be used to register and unregister connections.
type RPCClientFunc func(context.Context, io.ReadWriteCloser, *zerolog.Logger) NamedTunnelRPCClient

type controlStream struct {
	observer *Observer

	connectedFuse     ConnectedFuse
	namedTunnelConfig *NamedTunnelConfig
	connIndex         uint8

	newRPCClientFunc RPCClientFunc

	gracefulShutdownC <-chan struct{}
	gracePeriod       time.Duration
	stoppedGracefully bool
}

// ControlStreamHandler registers connections with origintunneld and initiates graceful shutdown.
type ControlStreamHandler interface {
	ServeControlStream(ctx context.Context, rw io.ReadWriteCloser, connOptions *tunnelpogs.ConnectionOptions) error
	IsStopped() bool
}

// NewControlStream returns a new instance of ControlStreamHandler
func NewControlStream(
	observer *Observer,
	connectedFuse ConnectedFuse,
	namedTunnelConfig *NamedTunnelConfig,
	connIndex uint8,
	newRPCClientFunc RPCClientFunc,
	gracefulShutdownC <-chan struct{},
	gracePeriod time.Duration,
) ControlStreamHandler {
	if newRPCClientFunc == nil {
		newRPCClientFunc = newRegistrationRPCClient
	}
	return &controlStream{
		observer:          observer,
		connectedFuse:     connectedFuse,
		namedTunnelConfig: namedTunnelConfig,
		newRPCClientFunc:  newRPCClientFunc,
		connIndex:         connIndex,
		gracefulShutdownC: gracefulShutdownC,
		gracePeriod:       gracePeriod,
	}
}

func (c *controlStream) ServeControlStream(
	ctx context.Context,
	rw io.ReadWriteCloser,
	connOptions *tunnelpogs.ConnectionOptions,
) error {
	rpcClient := c.newRPCClientFunc(ctx, rw, c.observer.log)
	defer rpcClient.Close()

	if err := rpcClient.RegisterConnection(ctx, c.namedTunnelConfig, connOptions, c.connIndex, c.observer); err != nil {
		return err
	}
	c.connectedFuse.Connected()

	// wait for connection termination or start of graceful shutdown
	select {
	case <-ctx.Done():
		break
	case <-c.gracefulShutdownC:
		c.stoppedGracefully = true
	}

	c.observer.sendUnregisteringEvent(c.connIndex)
	rpcClient.GracefulShutdown(ctx, c.gracePeriod)
	c.observer.log.Info().Uint8(LogFieldConnIndex, c.connIndex).Msg("Unregistered tunnel connection")
	return nil
}

func (c *controlStream) IsStopped() bool {
	return c.stoppedGracefully
}
