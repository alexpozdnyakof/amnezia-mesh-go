// Package mesh defines the user-facing error type, mirroring Python's
// MeshError: cli renders it as a json-aware failure message.
package mesh

import "fmt"

// Error is a user-facing error.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

// Errorf builds a MeshError with a formatted message.
func Errorf(format string, args ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}
