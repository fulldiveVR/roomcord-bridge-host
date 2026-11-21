// roomcord-bridge-host - A host process for the mautrix bridgev2 network connectors.
// Copyright (C) 2026 The roomcord-bridge-host authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, version 3 of the License.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-only

package login

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rs/xid"
	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/hosts"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/localmatrix"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

// Error is an API-level error with a wire error code and HTTP status.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func apiErr(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

const (
	processTimeout = 30 * time.Minute
	// callTimeout caps one Start or Submit call. Upstream connectors can wait without
	// limit, e.g. Telegram keeps reconnecting when the API id is rejected.
	callTimeout = 60 * time.Second
	finishedTTL = 10 * time.Minute
)

type Engine struct {
	reg *hosts.Registry
	log zerolog.Logger

	mu    sync.Mutex
	procs map[string]*Process
}

func NewEngine(reg *hosts.Registry, log zerolog.Logger) *Engine {
	return &Engine{reg: reg, log: log, procs: make(map[string]*Process)}
}

type Process struct {
	ID      string
	Network string
	UserID  string

	net      *hosts.Network
	proc     bridgev2.LoginProcess
	override *bridgev2.UserLogin
	ctx      context.Context
	cancel   context.CancelFunc
	log      zerolog.Logger

	mu       sync.Mutex
	step     *bridgev2.LoginStep
	wire     wire.Step
	busy     bool
	waiting  bool
	finished bool
}

// ParseLoginKey splits a wire loginId into network and upstream user login id.
func ParseLoginKey(key string) (network string, loginID networkid.UserLoginID, ok bool) {
	n, l, ok := strings.Cut(key, ":")
	return n, networkid.UserLoginID(l), ok && n != "" && l != ""
}

func (e *Engine) Start(ctx context.Context, network, userID, flowID, reconnectLoginID string) (*Process, wire.Step, error) {
	n := e.reg.Get(network)
	if reason, ok := e.reg.Unavailable()[network]; ok && n == nil {
		return nil, wire.Step{}, apiErr(503, "network_unavailable", "%s is not available: %s", network, reason)
	}
	if n == nil {
		return nil, wire.Step{}, apiErr(404, "network_not_found", "unknown network %q", network)
	}
	if userID == "" {
		return nil, wire.Step{}, apiErr(400, "invalid_input", "userId is required")
	}
	if !slices.ContainsFunc(n.Connector.GetLoginFlows(), func(f bridgev2.LoginFlow) bool { return f.ID == flowID }) {
		return nil, wire.Step{}, apiErr(400, "unsupported_method", "unknown flow %q for network %q", flowID, network)
	}
	if n.ConfigError != nil {
		return nil, wire.Step{}, apiErr(503, "network_unavailable", "%s is not configured: %s", network, n.ConfigError)
	}
	br := n.Bridge
	user, err := br.GetUserByMXID(ctx, localmatrix.UserMXID(userID))
	if err != nil {
		return nil, wire.Step{}, err
	}
	var override *bridgev2.UserLogin
	if reconnectLoginID != "" {
		ln, lid, ok := ParseLoginKey(reconnectLoginID)
		if !ok || ln != network {
			return nil, wire.Step{}, apiErr(404, "account_not_found", "unknown login %q", reconnectLoginID)
		}
		override, err = br.GetExistingUserLoginByID(ctx, lid)
		if err != nil {
			return nil, wire.Step{}, err
		} else if override == nil || override.UserMXID != user.MXID {
			return nil, wire.Step{}, apiErr(404, "account_not_found", "unknown login %q", reconnectLoginID)
		}
	}
	proc, err := n.Connector.CreateLogin(ctx, user, flowID)
	if err != nil {
		return nil, wire.Step{}, err
	}
	p := &Process{
		ID:       "lp_" + xid.New().String(),
		Network:  network,
		UserID:   userID,
		net:      n,
		proc:     proc,
		override: override,
	}
	p.log = e.log.With().Str("login_process", p.ID).Str("network", network).Str("flow", flowID).Logger()
	p.ctx, p.cancel = context.WithTimeout(p.log.WithContext(br.BackgroundCtx), processTimeout)
	e.mu.Lock()
	e.procs[p.ID] = p
	e.mu.Unlock()
	go func() {
		<-p.ctx.Done()
		if errors.Is(p.ctx.Err(), context.DeadlineExceeded) {
			p.mu.Lock()
			if !p.finished {
				p.wire = wire.Step{StepID: "failed", Kind: wire.KindFailed, Error: &wire.StepError{Code: "expired", Message: "Login process timed out"}}
			}
			p.mu.Unlock()
			e.finish(p, nil, true)
		}
	}()

	var step *bridgev2.LoginStep
	params := bridgev2.LoginStartParams{Override: override}
	startCtx, cancelStart := context.WithTimeout(p.ctx, callTimeout)
	defer cancelStart()
	if withParams, ok := proc.(bridgev2.LoginProcessWithParams); ok {
		step, err = withParams.StartWithParams(startCtx, params)
	} else if withOverride, ok := proc.(bridgev2.LoginProcessWithOverride); ok && override != nil {
		step, err = withOverride.StartWithOverride(startCtx, override)
	} else {
		step, err = proc.Start(startCtx)
	}
	p.mu.Lock()
	e.apply(p, step, err)
	out := p.wire
	p.mu.Unlock()
	return p, out, nil
}

func (e *Engine) Get(processID string) (wire.Step, error) {
	p := e.get(processID)
	if p == nil {
		return wire.Step{}, apiErr(404, "connect_session_not_found", "unknown login process %q", processID)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.wire, nil
}

func (e *Engine) get(processID string) *Process {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.procs[processID]
}

// Input is the body of a step submit. Fields answers an input step. Cookies or
// Paste answers a capture step.
type Input struct {
	Fields  map[string]string
	Cookies map[string]string
	Paste   string
}

func (e *Engine) Submit(ctx context.Context, processID, stepID string, in Input) (wire.Step, error) {
	p := e.get(processID)
	if p == nil {
		return wire.Step{}, apiErr(404, "connect_session_not_found", "unknown login process %q", processID)
	}
	p.mu.Lock()
	if p.finished {
		defer p.mu.Unlock()
		return p.wire, nil
	}
	if p.busy || p.wire.StepID != stepID {
		defer p.mu.Unlock()
		return wire.Step{}, apiErr(409, "stale_step", "step %q is not the current step %q", stepID, p.wire.StepID)
	}
	current := p.step
	var run func(context.Context) (*bridgev2.LoginStep, error)
	switch current.Type {
	case bridgev2.LoginStepTypeUserInput:
		input, verr := validateInput(current.UserInputParams.Fields, in.Fields)
		if verr != nil {
			p.wire.Error = verr
			defer p.mu.Unlock()
			return p.wire, nil
		}
		ui, ok := p.proc.(bridgev2.LoginProcessUserInput)
		if !ok {
			p.mu.Unlock()
			return wire.Step{}, apiErr(500, "internal_error", "login process does not accept user input")
		}
		run = func(ctx context.Context) (*bridgev2.LoginStep, error) { return ui.SubmitUserInput(ctx, input) }
	case bridgev2.LoginStepTypeCookies:
		ck, ok := p.proc.(bridgev2.LoginProcessCookies)
		if !ok {
			p.mu.Unlock()
			return wire.Step{}, apiErr(500, "internal_error", "login process does not accept cookies")
		}
		values := in.Cookies
		if values == nil {
			values = in.Fields
		}
		// A rejected capture keeps the upstream step: the client can submit again.
		cookies, verr := captureInput(current.CookiesParams, values, in.Paste)
		if verr != nil {
			p.wire.Error = verr
			defer p.mu.Unlock()
			return p.wire, nil
		}
		run = func(ctx context.Context) (*bridgev2.LoginStep, error) { return ck.SubmitCookies(ctx, cookies) }
	default:
		defer p.mu.Unlock()
		return p.wire, nil
	}
	p.busy = true
	p.mu.Unlock()

	callCtx, cancelCall := context.WithTimeout(p.ctx, callTimeout)
	step, err := run(callCtx)
	cancelCall()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy = false
	if p.finished {
		return p.wire, nil
	}
	e.apply(p, step, err)
	return p.wire, nil
}

func (e *Engine) Cancel(processID string) error {
	p := e.get(processID)
	if p == nil {
		return apiErr(404, "connect_session_not_found", "unknown login process %q", processID)
	}
	p.mu.Lock()
	finished := p.finished
	if !finished {
		p.wire = wire.Step{StepID: "failed", Kind: wire.KindFailed, Error: &wire.StepError{Code: "cancelled", Message: "Login process was cancelled"}}
	}
	p.mu.Unlock()
	e.finish(p, nil, !finished)
	e.mu.Lock()
	delete(e.procs, processID)
	e.mu.Unlock()
	return nil
}

// apply records the result of an upstream call. Caller holds p.mu.
func (e *Engine) apply(p *Process, step *bridgev2.LoginStep, err error) {
	if err == nil && step == nil {
		err = errors.New("connector returned neither a step nor an error")
	}
	if err != nil {
		p.wire = MapError(err)
		evt := p.log.Warn().Str("code", p.wire.Error.Code).Str("upstream_code", p.wire.Error.UpstreamCode)
		if p.step != nil && p.step.Type == bridgev2.LoginStepTypeCookies {
			// The error of a capture submit can quote a captured value: log its type only.
			evt = evt.Str("error_type", fmt.Sprintf("%T", err))
		} else {
			evt = evt.Err(err)
		}
		evt.Msg("Login step failed")
		go e.finish(p, nil, true)
		p.finished = true
		return
	}
	p.step = step
	p.wire = MapStep(step, func(id string) string { return p.Network + ":" + id })
	p.log.Debug().Str("step_id", step.StepID).Str("kind", p.wire.Kind).Msg("Login step")
	switch {
	case p.wire.Kind == wire.KindFailed:
		p.finished = true
		go e.finish(p, nil, true)
	case step.Type == bridgev2.LoginStepTypeComplete:
		p.finished = true
		go e.finish(p, step, false)
	case step.Type == bridgev2.LoginStepTypeDisplayAndWait && !p.waiting:
		p.waiting = true
		go e.waitLoop(p)
	}
}

func (e *Engine) waitLoop(p *Process) {
	defer func() {
		p.mu.Lock()
		p.waiting = false
		p.mu.Unlock()
	}()
	dw, ok := p.proc.(bridgev2.LoginProcessDisplayAndWait)
	if !ok {
		p.mu.Lock()
		e.apply(p, nil, errors.New("login process cannot wait"))
		p.mu.Unlock()
		return
	}
	for {
		step, err := dw.Wait(p.ctx)
		p.mu.Lock()
		if p.finished {
			p.mu.Unlock()
			return
		}
		p.waiting = false
		e.apply(p, step, err)
		cont := !p.finished && step != nil && step.Type == bridgev2.LoginStepTypeDisplayAndWait
		if cont {
			p.waiting = true
		}
		p.mu.Unlock()
		if !cont {
			return
		}
	}
}

// finish releases the process. On success it also replaces the overridden login
// when the account behind it changed, like the upstream provisioning API does.
func (e *Engine) finish(p *Process, complete *bridgev2.LoginStep, cancelProcess bool) {
	p.mu.Lock()
	p.finished = true
	p.mu.Unlock()
	if cancelProcess {
		p.proc.Cancel()
	}
	p.cancel()
	if complete != nil && p.override != nil && complete.CompleteParams != nil && p.override.ID != complete.CompleteParams.UserLoginID {
		p.override.Delete(p.log.WithContext(context.Background()), status.BridgeState{
			StateEvent: status.StateLoggedOut,
			Reason:     "LOGIN_OVERRIDDEN",
		}, bridgev2.DeleteOpts{LogoutRemote: true})
	}
	time.AfterFunc(finishedTTL, func() {
		e.mu.Lock()
		delete(e.procs, p.ID)
		e.mu.Unlock()
	})
}

func validateInput(fields []bridgev2.LoginInputDataField, input map[string]string) (map[string]string, *wire.StepError) {
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		f.FillDefaultValidate()
		val, err := f.Validate(input[f.ID])
		if err != nil || (val == "" && !f.Optional) {
			code := "invalid_input"
			switch f.Type {
			case bridgev2.LoginInputFieldTypePhoneNumber:
				code = "invalid_phone_number"
			case bridgev2.LoginInputFieldType2FACode:
				code = "invalid_code"
			case bridgev2.LoginInputFieldTypePassword:
				code = "invalid_password"
			}
			msg := fmt.Sprintf("%s is required", f.Name)
			if err != nil {
				msg = fmt.Sprintf("%s: %s", f.Name, err)
			}
			return nil, &wire.StepError{Code: code, Message: msg}
		}
		out[f.ID] = val
	}
	return out, nil
}
