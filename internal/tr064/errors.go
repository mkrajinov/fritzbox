package tr064

import (
	"errors"
	"fmt"
)

// ErrUnauthorized is returned when the box rejects the credentials (HTTP 401).
var ErrUnauthorized = errors.New("unauthorized: wrong user/password, or the user lacks the required FRITZ!Box permission")

// ErrUnknownService is returned when a service is not announced by the box.
var ErrUnknownService = errors.New("unknown service")

// UPnP error codes as documented by AVM (and mirrored by fritzconnection).
const (
	CodeInvalidAction       = 401
	CodeInvalidArgs         = 402
	CodeActionFailed        = 501
	CodeArgumentValue       = 600
	CodeOutOfMemory         = 603
	CodeSecurity            = 606 // action not authorized: missing user right
	CodeArrayIndexInvalid   = 713
	CodeNoSuchEntry         = 714
	CodeStringTooShort      = 801
	CodeStringTooLong       = 802
	CodeInvalidCharacter    = 803
	CodeInternalError       = 820
	CodeSecondFactorNeeded  = 866 // X_AVM-DE_Auth: confirm on the box
	CodeSecondFactorBlocked = 867
	CodeSecondFactorBusy    = 868
	CodeNotAllowedForDevice = 880
)

// UPnPError is a SOAP fault returned by the box.
type UPnPError struct {
	Service, Action string
	Code            int
	Description     string
}

func (e *UPnPError) Error() string {
	hint := ""
	switch e.Code {
	case CodeSecurity:
		hint = " (the FRITZ!Box user lacks the permission for this action)"
	case CodeSecondFactorNeeded, CodeSecondFactorBlocked, CodeSecondFactorBusy:
		hint = " (" + SecondFactorHint + ")"
	case CodeNotAllowedForDevice:
		hint = " (not allowed for this device: FRITZ! devices and PLC adapters can't be blocked)"
	case CodeInvalidAction:
		hint = " (action not supported by this box/firmware)"
	}
	return fmt.Sprintf("%s.%s: UPnP error %d %s%s", e.Service, e.Action, e.Code, e.Description, hint)
}

// IsCode reports whether err is a UPnPError with the given code.
func IsCode(err error, code int) bool {
	var u *UPnPError
	return errors.As(err, &u) && u.Code == code
}

// SecondFactorHint explains a second-factor error that reached the user, i.e. confirmation was
// not given in time, was refused, or is blocked after too many attempts.
const SecondFactorHint = "the box requires a confirmation on the device (button or phone code) for this action; " +
	"it was not confirmed in time, or confirmations are temporarily blocked"

// NeedsSecondFactor reports whether the box wants a physical confirmation.
func NeedsSecondFactor(err error) bool {
	return IsCode(err, CodeSecondFactorNeeded) || IsCode(err, CodeSecondFactorBlocked) || IsCode(err, CodeSecondFactorBusy)
}
