package main

import (
	"errors"
	"expvar"
	"fmt"
	"greenlight/internal/data"
	"greenlight/internal/validator"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tomasen/realip"
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
			ip := realip.FromRequest(r)

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

		}
		next.ServeHTTP(w, r)
	})
}

func (app *application) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Indicate to any caches that the response may vary based on value of
		// Authorization header in the req.
		w.Header().Add("Vary", "Authorization")

		// Get value of the Authorization header from the req.
		authorizationHeader := r.Header.Get("Authorization")

		// If no auth header, add anon user to req ctx. Then call next handler in
		// chain and not run anything else below here.
		if authorizationHeader == "" {
			r = app.contextSetUser(r, data.AnonymousUser)
			next.ServeHTTP(w, r)
			return
		}

		// We expect "Bearer <token>".
		headerParts := strings.Split(authorizationHeader, " ")
		if len(headerParts) != 2 || headerParts[0] != "Bearer" {
			app.invalidAuthenticationTokenResponse(w, r)
			return
		}

		// Extract the actual token.
		token := headerParts[1]

		// Validate token
		v := validator.New()

		if data.ValidateTokenPlaintext(v, token); !v.Valid() {
			app.invalidAuthenticationTokenResponse(w, r)
			return
		}

		// Get user associated with token. (Remember to use scopeAuthentication)
		user, err := app.models.Users.GetForToken(data.ScopeAuthentication, token)
		if err != nil {
			switch {
			case errors.Is(err, data.ErrRecordNotFound):
				app.invalidAuthenticationTokenResponse(w, r)
			default:
				app.serverErrorResponse(w, r, err)
			}
			return
		}

		// Add the user info to the req ctx.
		r = app.contextSetUser(r, user)

		// Call next handler in chain.
		next.ServeHTTP(w, r)
	})
}

// Different signature - so we can wrap handler funcs directly with this middleware.

// Checks if user is not anon.
func (app *application) requireAuthenticatedUser(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := app.contextGetUser(r)

		if user.IsAnonymous() {
			app.authenticationRequiredResponse(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Checks that user if both authenticated and activated.
func (app *application) requireActivatedUser(next http.HandlerFunc) http.HandlerFunc {
	fn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := app.contextGetUser(r)

		if !user.Activated {
			app.inactiveAccountResponse(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})

	// Wrap in requireAuthenticatedUser middleware which runs FIRST.
	return app.requireAuthenticatedUser(fn)
}

func (app *application) requirePermission(code string, next http.HandlerFunc) http.HandlerFunc {
	fn := func(w http.ResponseWriter, r *http.Request) {
		// Retrieve user from current ctx.
		user := app.contextGetUser(r)

		// Get the slice of permissions for user.
		permissions, err := app.models.Permissions.GetAllForUser(user.ID)
		if err != nil {
			app.serverErrorResponse(w, r, err)
			return
		}

		// Check if the slice includes the required perm. If not, 403.
		if !permissions.Include(code) {
			app.notPermittedResponse(w, r)
			return
		}

		// Check successful, call next handler in the chain.
		next.ServeHTTP(w, r)
	}

	return app.requireActivatedUser(fn)
}

func (app *application) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add Vary header.
		w.Header().Add("Vary", "Origin")
		w.Header().Add("Vary", "Access-Control-Request-Method")

		// Get value of the req's origin header
		origin := r.Header.Get("Origin")

		// Only run this if there's an origin
		if origin != "" {
			for i := range app.config.cors.trustedOrigins {
				if origin == app.config.cors.trustedOrigins[i] {
					w.Header().Set("Access-Control-Allow-Origin", origin)

					// Check if it's a preflight request.
					if r.Method == http.MethodOptions &&
						r.Header.Get("Access-Control-Request-Method") != "" {
						// Set the necessary preflight response headers.
						w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, PUT, PATCH, DELETE")
						w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
						w.Header().Set("Access-Control-Max-Age", "60")

						// Write headers along a 200 OK and return from middleware.
						w.WriteHeader(http.StatusOK)
						return
					}

					break
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

type metricsResponseWriter struct {
	wrapped       http.ResponseWriter
	statusCode    int
	headerWritten bool
}

func (mw *metricsResponseWriter) Header() http.Header {
	return mw.wrapped.Header()
}

func (mw *metricsResponseWriter) WriteHeader(statusCode int) {
	mw.wrapped.WriteHeader(statusCode)

	if !mw.headerWritten {
		mw.statusCode = statusCode
		mw.headerWritten = true
	}
}

func (mw *metricsResponseWriter) Write(b []byte) (int, error) {
	if !mw.headerWritten {
		mw.statusCode = http.StatusOK
		mw.headerWritten = true
	}

	return mw.wrapped.Write(b)
}

func (mw *metricsResponseWriter) Unwrap() http.ResponseWriter {
	return mw.wrapped
}

func (app *application) metrics(next http.Handler) http.Handler {
	// Init new expvar vars when the middleware chain is first built.
	var (
		totalRequestsReceived           = expvar.NewInt("total_requests_received")
		totalResponsesSent              = expvar.NewInt("total_responses_sent")
		totalProcessingTimeMicroseconds = expvar.NewInt("total_processing_time_μs")
		totalResponsesSentByStatus      = expvar.NewMap("total_responses_sent_by_status")
	)

	// For every request...
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Starting to process the request.
		start := time.Now()

		// Incr num of reqs by 1.
		totalRequestsReceived.Add(1)

		mw := &metricsResponseWriter{wrapped: w}

		// Call next handler in chain using the new metricsResponseWriter
		next.ServeHTTP(mw, r)

		// On the way back up the middleware chain, increment number of responses.
		totalResponsesSent.Add(1)

		// At this point, the res status code should be stored in mw.statusCode field.
		totalResponsesSentByStatus.Add(strconv.Itoa(mw.statusCode), 1)

		// Get time since we began to process the request.
		duration := time.Since(start).Microseconds()
		totalProcessingTimeMicroseconds.Add(duration)
	})
}
