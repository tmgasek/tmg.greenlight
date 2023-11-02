package main

import (
	"context"
	"greenlight/internal/data"
	"net/http"
)

type contextKey string

// Convert string "user" to be a contextKey type and assign to userContextKey const.
// This is what we'll use as key
const userContextKey = contextKey("user")

// Returns a new copy of the request with the provided User struct added to ctx.
func (app *application) contextSetUser(r *http.Request, user *data.User) *http.Request {
	ctx := context.WithValue(r.Context(), userContextKey, user)
	return r.WithContext(ctx)
}

// Retrieves the User struct from the req ctx. We only gonna use this when we
// expect a user struct val in ctx, so OK to panic if not there.
func (app *application) contextGetUser(r *http.Request) *data.User {
	// Need to cast.
	user, ok := r.Context().Value(userContextKey).(*data.User)
	if !ok {
		panic("missing user value in req ctx")
	}

	return user
}
