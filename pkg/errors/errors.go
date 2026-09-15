package errors

import (
	"errors"
	"fmt"
)

// APIError is a business error carrying a stable Code, a human readable
// message and an optional wrapped cause. It is the single error type
// returned by service business logic and rendered into the unified API
// envelope.
type APIError struct {
	Code    Code
	Message string
	cause   error
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("[%d] %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Unwrap exposes the wrapped cause for errors.Is / errors.As.
func (e *APIError) Unwrap() error { return e.cause }

// New builds an APIError with the canonical message for the given code.
func New(code Code) *APIError {
	return &APIError{Code: code, Message: Message(code)}
}

// Newf builds an APIError with a formatted message overriding the canonical one.
func Newf(code Code, format string, args ...any) *APIError {
	return &APIError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds an APIError with a formatted message that also wraps cause.
func Wrap(code Code, cause error, format string, args ...any) *APIError {
	return &APIError{Code: code, Message: fmt.Sprintf(format, args...), cause: cause}
}

// WithCause attaches a cause to an existing APIError (chainable).
func (e *APIError) WithCause(cause error) *APIError {
	e.cause = cause
	return e
}

// As extracts an *APIError from err if present.
func As(err error) (*APIError, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// CodeOf returns the business code for err, defaulting to CodeInternal for
// non-APIError values and CodeOK for nil.
func CodeOf(err error) Code {
	if err == nil {
		return CodeOK
	}
	if ae, ok := As(err); ok {
		return ae.Code
	}
	return CodeInternal
}

// MessageOf returns a client-safe message for err.
func MessageOf(err error) string {
	if err == nil {
		return Message(CodeOK)
	}
	if ae, ok := As(err); ok {
		return ae.Message
	}
	return Message(CodeInternal)
}
