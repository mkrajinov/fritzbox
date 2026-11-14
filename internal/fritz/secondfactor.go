package fritz

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mkrajinovic/fbctl/internal/tr064"
)

// Second-factor confirmation (X_AVM-DE_Auth, AVM "TR-064 Authentication" v5):
// a write fails with 866 → SetConfig(start) returns a token and the allowed methods → the user
// presses a button on the box or dials a code → GetState reports "authenticated" → the original
// call is repeated with the token in the SOAP header.

const (
	authService = "X_AVM-DE_Auth1"
	// SecondFactorTimeout is how long fb waits for the user to confirm on the box.
	SecondFactorTimeout = 2 * time.Minute
	authPoll            = time.Second
)

// SecondFactorPrompt tells the user how to confirm. Abort stops waiting.
type SecondFactorPrompt struct {
	Router  string
	Action  string // service.action that asked for confirmation
	Methods []AuthMethod
	Abort   func()
}

// AuthMethod is one way to confirm.
type AuthMethod struct {
	Kind string // "button", "dtmf", or what the box reported
	Code string // DTMF sequence for "dtmf"
}

func (m AuthMethod) String() string {
	switch m.Kind {
	case "button":
		return "press any button on the FRITZ!Box"
	case "dtmf":
		return "dial " + m.Code + " on a phone connected to the box"
	}
	return m.Kind
}

// Instructions renders the methods as one sentence.
func (p SecondFactorPrompt) Instructions() string {
	parts := make([]string, 0, len(p.Methods))
	for _, m := range p.Methods {
		parts = append(parts, m.String())
	}
	if len(parts) == 0 {
		return "confirm on the FRITZ!Box"
	}
	return strings.Join(parts, ", or ")
}

// ParseAuthMethods parses NewMethods, e.g. "button,dtmf;*1234".
func ParseAuthMethods(s string) []AuthMethod {
	var out []AuthMethod
	for f := range strings.SplitSeq(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		kind, code, _ := strings.Cut(f, ";")
		out = append(out, AuthMethod{Kind: strings.ToLower(strings.TrimSpace(kind)), Code: strings.TrimSpace(code)})
	}
	return out
}

// SetSecondFactorHandler installs the callback that shows the confirmation prompt. Without a
// handler, 866 errors are returned as they are.
func (b *Box) SetSecondFactorHandler(f func(SecondFactorPrompt)) {
	b.sfMu.Lock()
	b.onSecondFactor = f
	b.sfMu.Unlock()
}

func (b *Box) secondFactorHandler() func(SecondFactorPrompt) {
	b.sfMu.Lock()
	defer b.sfMu.Unlock()
	return b.onSecondFactor
}

// ErrSecondFactorAborted is returned when the user cancels the confirmation.
var ErrSecondFactorAborted = errors.New("confirmation on the box cancelled")

// call runs an action and, when the box asks for a second factor, walks the user through it.
func (b *Box) call(ctx context.Context, svc, action string, args tr064.Args) (tr064.Result, error) {
	r, err := b.TR.Call(ctx, svc, action, args)
	if !tr064.IsCode(err, tr064.CodeSecondFactorNeeded) {
		return r, err
	}
	notify := b.secondFactorHandler()
	if notify == nil {
		return r, err
	}
	// The user needs longer than a normal request timeout to walk to the box; keep cancellation
	// (ctrl+c, esc) but replace the deadline.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), SecondFactorTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cancel()
		}
	})
	defer stop()

	start, aerr := b.TR.Call(wctx, authService, "SetConfig", tr064.Args{"NewAction": "start"})
	if aerr != nil {
		return nil, fmt.Errorf("%w; starting the confirmation failed: %v", err, aerr)
	}
	token := start["NewToken"]
	var aborted atomic.Bool
	notify(SecondFactorPrompt{
		Router: b.Name, Action: svc + "." + action, Methods: ParseAuthMethods(start["NewMethods"]),
		Abort: func() { aborted.Store(true); cancel() },
	})
	state := start["NewState"]
	t := time.NewTicker(authPoll)
	defer t.Stop()
	for state != "authenticated" {
		switch state {
		case "", "waitingforauth":
		default: // stopped, blocked, failure
			return nil, fmt.Errorf("confirmation on the box: %s", state)
		}
		select {
		case <-wctx.Done():
			// Tell the box to stop waiting; best effort with a fresh short context.
			sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = b.TR.CallWithToken(sctx, authService, "SetConfig", tr064.Args{"NewAction": "stop"}, token)
			scancel()
			if aborted.Load() || ctx.Err() != nil {
				return nil, ErrSecondFactorAborted
			}
			return nil, fmt.Errorf("not confirmed on the box within %s", SecondFactorTimeout)
		case <-t.C:
		}
		st, err := b.TR.CallWithToken(wctx, authService, "GetState", nil, token)
		if err != nil {
			if wctx.Err() != nil {
				continue // handled by the select above
			}
			return nil, err
		}
		state = st["NewState"]
	}
	return b.TR.CallWithToken(wctx, svc, action, args, token)
}
