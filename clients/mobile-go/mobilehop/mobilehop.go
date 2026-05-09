// Package mobilehop is the gomobile-bind entry point for hopssh's mobile
// clients. It wraps the internal/client package's Client API with a layer
// that satisfies gomobile's stricter typing rules:
//
//   - No <-chan returns (gomobile cannot bind channels). Mobile callers
//     receive events via an EventCallback registered through
//     SubscribeCallback; the wrapper hides Subscribe entirely.
//   - No struct fields with stdlib interface types (io.Writer, net.Listener).
//     The wrapper never accepts Config — instead NewSession takes a
//     ConfigDir string and an optional UserAgent + heartbeat seconds.
//   - No Snapshot / PeerInfo slice returns crossing the binding —
//     Status() and Peers() return JSON strings instead. Mobile callers
//     decode using their platform's standard library (Foundation /
//     org.json) rather than relying on gomobile's struct binding.
//   - No InstanceHTTPHook (the agent-mux is desktop-only). Mobile clients
//     never expose /exec / /upload / /shell on the mesh, so the hook
//     stays nil — handled inside the wrapper.
//
// The wrapper preserves Connect/Disconnect/Enroll/Leave/ForceRenew
// 1:1 with the underlying *client.Client. Lifecycle: NewSession →
// Start(ctx) → ... → Stop().
//
// Build:
//
//	cd clients/mobile-go && make ios-xcframework  # produces dist/MobileHop.xcframework
//	cd clients/mobile-go && make android-aar      # produces dist/mobilehop.aar
//
// The artifacts are NOT shipped — they're a build-tripwire that proves
// the FFI surface is genuinely consumable by gomobile (validating the
// reflection-based constraints in internal/client/api_constraints_test.go).
package mobilehop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/trustos/hopssh/internal/client"
)

// Session is the mobile-bound handle. One per mobile process.
//
// Maps 1:1 to internal/client.Client but exposes a gomobile-compatible
// API surface. Methods that would be gomobile-incompatible on Client
// (channel returns, slice-of-struct returns) are reshaped here.
type Session struct {
	c       *client.Client
	mu      sync.Mutex
	runCtx  context.Context
	cancel  context.CancelFunc
	started bool

	subMu  sync.Mutex
	subs   map[int64]client.SubscriptionID
	nextID int64
}

// NewSession constructs a Session backed by a fresh *client.Client.
// configDir is the on-disk root for enrollments.json + per-enrollment
// subdirs. heartbeatSeconds defaults to 60 if 0. userAgent is sent
// with outbound heartbeats; pass an empty string to use the default.
//
// gomobile-bind shape: returns (*Session, error). Both types bind cleanly.
func NewSession(configDir, userAgent string, heartbeatSeconds int) (*Session, error) {
	if configDir == "" {
		return nil, errors.New("mobilehop.NewSession: configDir is required")
	}
	cfg := client.Config{
		ConfigDir: configDir,
		UserAgent: userAgent,
	}
	if heartbeatSeconds > 0 {
		cfg.HeartbeatInterval = time.Duration(heartbeatSeconds) * time.Second
	}
	if err := client.MigrateLegacyLayout(configDir); err != nil {
		return nil, err
	}
	c, err := client.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Session{c: c, subs: map[int64]client.SubscriptionID{}}, nil
}

// Start spawns goroutines for all live enrollments + watchdogs.
// Idempotent. Mobile callers typically call this once after NewSession
// then leave the session running for the lifetime of the VPN extension.
func (s *Session) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	s.runCtx, s.cancel = context.WithCancel(context.Background())
	if err := s.c.Start(s.runCtx); err != nil {
		s.cancel()
		s.runCtx, s.cancel = nil, nil
		return err
	}
	s.started = true
	return nil
}

// Stop tears down all instances and watchdogs.
func (s *Session) Stop() error {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.runCtx = nil
	s.started = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.c.Stop()
}

// Connect brings up a single enrollment.
func (s *Session) Connect(name string) error {
	return s.c.Connect(s.ctx(), name)
}

// Disconnect tears down a single enrollment.
func (s *Session) Disconnect(name string) error {
	return s.c.Disconnect(s.ctx(), name)
}

// Enroll runs a token-flow enrollment. The other modes (device-flow +
// bundle) require richer UX flows that mobile clients drive themselves;
// expose token-flow here as the mechanical primitive.
//
// gomobile-bind shape: scalar args + (string, error) return.
func (s *Session) Enroll(endpoint, name, token string) (string, error) {
	return s.c.Enroll(s.ctx(), client.EnrollOptions{
		Endpoint: endpoint,
		Name:     name,
		Token:    token,
	})
}

// Leave disconnects + removes a network.
func (s *Session) Leave(name string) error {
	return s.c.Leave(s.ctx(), name)
}

// StatusJSON returns the Snapshot as a JSON string. Mobile callers
// decode using their platform's JSON library — avoids gomobile's
// slice-of-struct binding limitations.
func (s *Session) StatusJSON() (string, error) {
	snap := s.c.Status()
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// PeersJSON returns the peer list for one enrollment as a JSON string.
func (s *Session) PeersJSON(name string) (string, error) {
	peers, err := s.c.Peers(name)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(peers)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ForceRenew triggers an inline cert renewal for one enrollment.
func (s *Session) ForceRenew(name string) error {
	return s.c.ForceRenew(s.ctx(), name)
}

// EnrollmentNamesCSV returns live enrollment names as a comma-separated
// string. Mobile callers split on ',' to get the slice — gomobile
// doesn't bind []string returns cleanly across all targets.
func (s *Session) EnrollmentNamesCSV() string {
	names := s.c.EnrollmentNames()
	if len(names) == 0 {
		return ""
	}
	out := names[0]
	for _, n := range names[1:] {
		out += "," + n
	}
	return out
}

// EventListener is the mobile-bound version of client.EventCallback.
// gomobile binds Go interfaces to Java/Swift abstract classes; the
// platform implements OnEvent and the bind layer marshals across.
type EventListener interface {
	OnEvent(at int64, eventType, enrollment, severity, message string)
}

// SubscribeEvents registers a listener. Returns a subscription handle
// (an int64; gomobile binds simple scalars best). Pass to Unsubscribe
// to detach.
func (s *Session) SubscribeEvents(l EventListener) int64 {
	cb := callbackAdapter{listener: l}
	id := s.c.SubscribeCallback(cb)
	s.subMu.Lock()
	s.nextID++
	handle := s.nextID
	s.subs[handle] = id
	s.subMu.Unlock()
	return handle
}

// Unsubscribe removes a previously-registered listener.
func (s *Session) Unsubscribe(handle int64) {
	s.subMu.Lock()
	id, ok := s.subs[handle]
	delete(s.subs, handle)
	s.subMu.Unlock()
	if ok {
		s.c.Unsubscribe(id)
	}
}

// callbackAdapter bridges between the binding's mobile-compatible
// EventListener (scalars only) and internal/client.EventCallback (takes
// a struct).
type callbackAdapter struct {
	listener EventListener
}

func (a callbackAdapter) OnEvent(e client.Event) {
	a.listener.OnEvent(
		e.At.Unix(),
		e.Type,
		e.Enrollment,
		e.Severity,
		e.Message,
	)
}

// ctx returns the Session's run context if Started, otherwise a fresh
// background context.
func (s *Session) ctx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runCtx != nil {
		return s.runCtx
	}
	return context.Background()
}

// Version returns the substrate package version for diagnostic use
// (returned in mobile UI's About panel).
//
// gomobile-bind: the constant binds as a public method on the package.
func Version() string {
	return "phase-nn-substrate"
}
