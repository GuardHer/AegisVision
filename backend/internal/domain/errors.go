package domain

import (
	"fmt"
	"net/http"
)

// Stable business error codes mapped to frontend i18n messages.
const (
	CodeOK              = 0
	CodeInvalidArgument = 40001
	CodeUnauthorized    = 40101
	CodeForbidden       = 40301
	CodeNotFound        = 40401
	CodeConflict        = 40901
	CodeInternal        = 50001
)

// Standard gRPC status codes (duplicated as plain ints to keep domain zero-dependency).
const (
	GRPCCodeOK              = 0
	GRPCCodeInvalidArgument = 3
	GRPCCodeNotFound        = 5
	GRPCCodeAlreadyExists   = 6
	GRPCCodePermissionDen   = 7
	GRPCCodeInternal        = 13
	GRPCCodeUnauthenticated = 16
)

// AppError represents a structured domain error safe to expose to clients.
// Raw SQL, file paths, or stack traces must only be stored in Err for internal logs.
type AppError struct {
	Code       int
	HTTPStatus int
	GRPCCode   int
	Message    string
	Err        error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("code=%d msg=%s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("code=%d msg=%s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// NewInvalidArgumentError constructs a 400 domain error.
func NewInvalidArgumentError(message string, err error) *AppError {
	return &AppError{
		Code:       CodeInvalidArgument,
		HTTPStatus: http.StatusBadRequest,
		GRPCCode:   GRPCCodeInvalidArgument,
		Message:    message,
		Err:        err,
	}
}

// NewUnauthorizedError constructs a 401 domain error.
func NewUnauthorizedError(message string, err error) *AppError {
	return &AppError{
		Code:       CodeUnauthorized,
		HTTPStatus: http.StatusUnauthorized,
		GRPCCode:   GRPCCodeUnauthenticated,
		Message:    message,
		Err:        err,
	}
}

// NewForbiddenError constructs a 403 domain error.
func NewForbiddenError(message string, err error) *AppError {
	return &AppError{
		Code:       CodeForbidden,
		HTTPStatus: http.StatusForbidden,
		GRPCCode:   GRPCCodePermissionDen,
		Message:    message,
		Err:        err,
	}
}

// NewNotFoundError constructs a 404 domain error.
func NewNotFoundError(message string, err error) *AppError {
	return &AppError{
		Code:       CodeNotFound,
		HTTPStatus: http.StatusNotFound,
		GRPCCode:   GRPCCodeNotFound,
		Message:    message,
		Err:        err,
	}
}

// NewConflictError constructs a 409 domain error.
func NewConflictError(message string, err error) *AppError {
	return &AppError{
		Code:       CodeConflict,
		HTTPStatus: http.StatusConflict,
		GRPCCode:   GRPCCodeAlreadyExists,
		Message:    message,
		Err:        err,
	}
}

// NewInternalError constructs a 500 domain error.
func NewInternalError(message string, err error) *AppError {
	return &AppError{
		Code:       CodeInternal,
		HTTPStatus: http.StatusInternalServerError,
		GRPCCode:   GRPCCodeInternal,
		Message:    message,
		Err:        err,
	}
}
