package service

import "errors"

// InputError marks an error caused by the caller's input, such as a
// filter syntax error or an unknown field, as opposed to a failure of the
// index. The API answers it with 400.
type InputError struct{ Err error }

// Error returns the wrapped message.
func (e *InputError) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped error.
func (e *InputError) Unwrap() error { return e.Err }

// input wraps err as an InputError; nil stays nil.
func input(err error) error {
	if err == nil {
		return nil
	}
	return &InputError{Err: err}
}

// IsInputError reports whether err was caused by the caller's input.
func IsInputError(err error) bool {
	var ie *InputError
	return errors.As(err, &ie)
}
