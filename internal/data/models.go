package data

import (
	"database/sql"
	"errors"
)

var (
	ErrRecordNotFound = errors.New("record not found")
	ErrEditConflict   = errors.New("edit conflict")
)

// Models struct to wrap models.
type Models struct {
	Movies MovieModel
	// can do Movies interface {Insert(movie *Movie) error ... etc} if need mock
}

// For ease of use
func NewModels(db *sql.DB) Models {
	return Models{
		Movies: MovieModel{DB: db},
	}
}
