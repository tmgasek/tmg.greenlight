package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// When we receive a SIGINT or SIGTERM, instruct srv to stop accepting new HTTP
// reqs, give any in flight HTTP reqs a grace period of 20 secs befor srv dies.

func (app *application) serve() error {

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", app.config.port),
		Handler:      app.routes(),
		ErrorLog:     log.New(app.logger, "", 0),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Use this chan to receive any errors returned by graceful Shutdown()
	shutdownError := make(chan error)

	// Start a bg goroutine to listen for signals.
	go func() {
		// Make a buffered quit channel.
		quit := make(chan os.Signal, 1)

		// Listen for oncoming signals and relay them to quit channel.
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

		// Read the signal from the quit channel. This will block until a signal
		// is received.
		s := <-quit

		app.logger.PrintInfo("shutting down server", map[string]string{
			"signal": s.String(),
		})

		// New context with a 20 sec timeout
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		shutdownError <- srv.Shutdown(ctx)
	}()

	app.logger.PrintInfo("starting server", map[string]string{
		"add": srv.Addr,
		"env": app.config.env,
	})

	err := srv.ListenAndServe()
	// If we get this err, indication that graceful shutdown has started - Good.
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	// Wait to receive return value from Shutdown() on shutdownError chan.
	err = <-shutdownError
	if err != nil {
		return err
	}

	// At this point, graceful shutdown successful.
	app.logger.PrintInfo("stopped server", map[string]string{
		"addr": srv.Addr,
	})

	return nil
}
