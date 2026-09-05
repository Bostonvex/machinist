package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// shutdownTimeout is how long an in-flight request has to finish once the
// collector is stopping. Reads here are short; a live stream is not, and it is
// closed rather than waited for.
const shutdownTimeout = 5 * time.Second

// Serve runs the collector on listen until ctx is cancelled.
//
// The address is checked by Listen, which refuses anything but loopback. The
// callback is handed the bound address rather than the configured one, so a
// caller that asked for port 0 learns which port it got.
func (s *Server) Serve(ctx context.Context, listen string, onListening func(net.Addr)) error {
	listener, err := Listen(listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	defer listener.Close()
	if onListening != nil {
		onListening(listener.Addr())
	}
	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		// No write or idle timeout: the live stream is a response that stays
		// open on purpose, and a deadline would cut it at a fixed interval
		// rather than when the reader goes away.
	}
	done := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			// A stream still open past the grace period is closed rather than
			// waited on. It has no end of its own to reach.
			shutdownErr = errors.Join(shutdownErr, httpServer.Close())
		}
		closeErr := listener.Close()
		if closeErr != nil && errors.Is(closeErr, net.ErrClosed) {
			closeErr = nil
		}
		<-done
		if shutdownErr != nil || closeErr != nil {
			return fmt.Errorf("stop the collector: %w", errors.Join(shutdownErr, closeErr))
		}
		return nil
	}
}
