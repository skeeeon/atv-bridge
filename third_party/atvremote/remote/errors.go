package remote

import "fmt"

// CannotConnect is returned when a connection to the Android TV cannot be
// established, e.g. an invalid IP address or the device being offline.
type CannotConnect struct {
	Message string
	Err     error
}

func (e *CannotConnect) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s", e.Message, e.Err)
	}
	return e.Message
}

func (e *CannotConnect) Unwrap() error { return e.Err }

// ConnectionClosed is returned when the connection to the Android TV was
// closed or lost while an operation was in progress.
type ConnectionClosed struct {
	Message string
	Err     error
}

func (e *ConnectionClosed) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s", e.Message, e.Err)
	}
	return e.Message
}

func (e *ConnectionClosed) Unwrap() error { return e.Err }

// InvalidAuth is returned when the Android TV rejects the client certificate
// (pairing is needed) or when pairing itself fails.
type InvalidAuth struct {
	Message string
	Err     error
}

func (e *InvalidAuth) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s", e.Message, e.Err)
	}
	return e.Message
}

func (e *InvalidAuth) Unwrap() error { return e.Err }
