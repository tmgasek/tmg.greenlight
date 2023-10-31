package main

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

func (app *application) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create a deferred func which will always be run in the event of a panic,
		// as Go unwinds the stack.

		defer func() {
			// Check if there was a panic.
			if err := recover(); err != nil {
				w.Header().Set("Connection", "close")
				// Normalize into error, and use method which will end up using
				// our custom Logger at ERROR level, and send client a 500.
				app.serverErrorResponse(w, r, fmt.Errorf("%s", err))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func (app *application) rateLimit(next http.Handler) http.Handler {
	type client struct {
		limiter  *rate.Limiter
		lastSeen time.Time
	}

	var (
		mu      sync.Mutex
		clients = make(map[string]*client)
	)

	// Launch a bg goroutine which removes old entries from the clients map
	// once every minute
	go func() {
		for {
			time.Sleep(time.Minute)

			// Lock mutex to prevent any rate limiter checks from happening while
			// the cleanup is taking place
			mu.Lock()

			// Loop through all clients. If not seen in last 3 mins, remove.
			for ip, client := range clients {
				if time.Since(client.lastSeen) > 3*time.Minute {
					delete(clients, ip)
				}
			}

			// Unlock mutex when cleanup is done
			mu.Unlock()
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.config.limiter.enabled {
			// Extract client's IP addr from req.
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				app.serverErrorResponse(w, r, err)
				return
			}

			// Lock to prevent this code from being executed concurrently.
			mu.Lock()

			// Check to see if IP address already exists in map.
			if _, found := clients[ip]; !found {
				// Create and add a new client struct to map if doesn't already exist.
				clients[ip] = &client{
					limiter: rate.NewLimiter(rate.Limit(app.config.limiter.rps),
						app.config.limiter.burst),
				}
			}

			// Update the last seen time for client.
			clients[ip].lastSeen = time.Now()

			// When we call Allow(), one token will be consumed from bucket.
			// Concurrency safe
			if !clients[ip].limiter.Allow() {
				mu.Unlock()
				app.rateLimitExceededResponse(w, r)
				return
			}

			// Unlock the mutex before calling next handler. DON'T DEFER THIS.
			// That would mean that mutex isnt unlocked until all the handlers downstream
			// from this middleware also returned.
			mu.Unlock()

			next.ServeHTTP(w, r)

		}
	})
}
