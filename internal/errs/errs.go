// Package errs defines the domain's error classes and the corrective action
// attached to each failure.
//
// The core never knows about exit codes: it returns classified errors, and
// only the command-line layer translates a class into a code. The Action field
// carries the exact command that resolves the situation, written so the agent
// can pass it on verbatim to the human.
package errs

import (
	"errors"
	"fmt"
)

// Class identifies the nature of a failure. Each class maps to a distinct,
// stable exit code.
type Class int

const (
	// ClassUsage means malformed input or incorrect use of the command.
	ClassUsage Class = iota
	// ClassNotFound means the skill does not exist at the source.
	ClassNotFound
	// ClassNotTrusted means the skill exists but was never approved. This is
	// deliberately distinct from ClassNotFound.
	ClassNotTrusted
	// ClassIntegrityMismatch means the content differs from what was approved.
	ClassIntegrityMismatch
	// ClassNetwork means a network failure with no usable cache.
	ClassNetwork
	// ClassTTYRequired means the command needs an interactive terminal.
	ClassTTYRequired
	// ClassState means local state is corrupt or has invalid permissions.
	ClassState
)

func (c Class) String() string {
	switch c {
	case ClassUsage:
		return "usage"
	case ClassNotFound:
		return "not found"
	case ClassNotTrusted:
		return "not trusted"
	case ClassIntegrityMismatch:
		return "integrity"
	case ClassNetwork:
		return "network"
	case ClassTTYRequired:
		return "terminal required"
	case ClassState:
		return "state"
	default:
		return "unknown"
	}
}

// Error is the domain's typed error.
type Error struct {
	Class  Class
	Msg    string
	Action string // corrective command, safe to pass on verbatim
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	return e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// New builds a typed error with no wrapped cause.
func New(class Class, action, format string, args ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, args...), Action: action}
}

// Wrap builds a typed error that chains a cause.
func Wrap(err error, class Class, action, format string, args ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, args...), Action: action, Err: err}
}

// Usage builds an incorrect-use error.
func Usage(action, format string, args ...any) *Error {
	return New(ClassUsage, action, format, args...)
}

// NotFound builds an error for a skill missing at the source.
func NotFound(action, format string, args ...any) *Error {
	return New(ClassNotFound, action, format, args...)
}

// NotTrusted builds an error for a skill that was never approved.
func NotTrusted(action, format string, args ...any) *Error {
	return New(ClassNotTrusted, action, format, args...)
}

// Integrity builds an integrity-mismatch error.
func Integrity(action, format string, args ...any) *Error {
	return New(ClassIntegrityMismatch, action, format, args...)
}

// Network builds a network-failure error.
func Network(action, format string, args ...any) *Error {
	return New(ClassNetwork, action, format, args...)
}

// TTYRequired builds an error for a command that needs an interactive terminal.
func TTYRequired(action, format string, args ...any) *Error {
	return New(ClassTTYRequired, action, format, args...)
}

// State builds an invalid-local-state error.
func State(action, format string, args ...any) *Error {
	return New(ClassState, action, format, args...)
}

// ClassOf extracts the class from an error chain. Returns false when the error
// is not a domain error.
func ClassOf(err error) (Class, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Class, true
	}
	return 0, false
}

// ActionOf extracts the corrective action from an error chain.
func ActionOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Action
	}
	return ""
}
