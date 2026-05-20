package lsp

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Host is the multi-language LSP coordinator: one process-wide owner
// of a set of Driver instances keyed by languageID. Host handles
// lazy spawn (pyright never starts unless we hit a .py file),
// crash-tolerant recovery (a dead Driver is reaped and the next call
// transparently re-Initializes), and orderly Shutdown on Close.
//
// Concurrency: callers may call DriverFor / Shutdown from multiple
// goroutines; per-driver mutation is serialised by the per-language
// mutex so Initialize doesn't race with DriverFor.
type Host struct {
	registry *Registry
	root     string

	mu            sync.Mutex
	drivers       map[string]Driver
	languageMutex map[string]*sync.Mutex
	closed        bool

	// notifHandler is installed at Host-scope; every Driver built
	// after this point inherits it on construction. P1.5.T02.
	notifHandler NotificationHandler
}

// SetNotificationHandler installs an LSP push-back callback at host
// scope. Drivers built after this call inherit the handler at spawn;
// drivers already running receive the handler immediately. P1.5.T02.
func (h *Host) SetNotificationHandler(handler NotificationHandler) {
	h.mu.Lock()
	h.notifHandler = handler
	drivers := make([]Driver, 0, len(h.drivers))
	for _, d := range h.drivers {
		drivers = append(drivers, d)
	}
	h.mu.Unlock()
	for _, d := range drivers {
		d.SetNotificationHandler(handler)
	}
}

// NewHost constructs a Host rooted at the given workspace path. The
// root is what each driver receives in its Initialize call; it should
// be the workspace directory the user opened, not the cwd.
func NewHost(registry *Registry, root string) *Host {
	if registry == nil {
		registry = NewRegistry()
	}
	return &Host{
		registry:      registry,
		root:          root,
		drivers:       make(map[string]Driver),
		languageMutex: make(map[string]*sync.Mutex),
	}
}

// DriverFor returns a ready-to-use Driver for languageID, lazily
// spawning + initializing it on first request. Returns an error if
// no driver is registered for the language or its executable is not
// on $PATH.
//
// If the cached Driver fails its Health check, DriverFor reaps it and
// returns a freshly initialized replacement — this is the
// restart-on-crash path SPEC §6.11 calls for ("LSP server lifecycle:
// stale connections, abrupt exits").
func (h *Host) DriverFor(ctx context.Context, languageID string) (Driver, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, fmt.Errorf("lsp: host closed")
	}
	mu, ok := h.languageMutex[languageID]
	if !ok {
		mu = &sync.Mutex{}
		h.languageMutex[languageID] = mu
	}
	h.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()

	h.mu.Lock()
	d := h.drivers[languageID]
	h.mu.Unlock()

	if d != nil {
		// Cheap health check; replace if dead.
		hctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		err := d.Health(hctx)
		cancel()
		if err == nil {
			return d, nil
		}
		// Dead — reap and fall through to respawn.
		_ = d.Shutdown(ctx)
		h.mu.Lock()
		delete(h.drivers, languageID)
		h.mu.Unlock()
	}

	if !h.registry.HasLanguage(languageID) {
		return nil, fmt.Errorf("lsp: no driver registered for language %q", languageID)
	}
	if _, ok := h.registry.LookupExecutable(languageID); !ok {
		return nil, fmt.Errorf("lsp: %s server not on $PATH", languageID)
	}
	d = h.registry.build(languageID)
	if d == nil {
		return nil, fmt.Errorf("lsp: registry returned nil driver for %q", languageID)
	}
	// Inherit host-scope notification handler before Initialize so the
	// driver's read loop has it from the first message. P1.5.T02.
	h.mu.Lock()
	notifHandler := h.notifHandler
	h.mu.Unlock()
	if notifHandler != nil {
		d.SetNotificationHandler(notifHandler)
	}
	if err := d.Initialize(ctx, h.root); err != nil {
		return nil, fmt.Errorf("lsp: initialize %s: %w", languageID, err)
	}
	h.mu.Lock()
	// Close may have run while Initialize was in flight; the map is
	// then nil and a bare assignment panics. Drop the freshly-built
	// driver instead and report host-closed.
	if h.closed {
		h.mu.Unlock()
		_ = d.Shutdown(ctx)
		return nil, fmt.Errorf("lsp: host closed")
	}
	h.drivers[languageID] = d
	h.mu.Unlock()
	return d, nil
}

// AvailableLanguages returns the languages whose drivers are
// installed in the current environment. Doesn't spawn anything — used
// by the doctor / surfaces to advertise capabilities.
func (h *Host) AvailableLanguages() []string {
	return h.registry.Available()
}

// Close shuts down every driver the Host has spawned. Idempotent;
// safe to call from a deferred context.
func (h *Host) Close(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	drivers := h.drivers
	h.drivers = nil
	h.mu.Unlock()
	var firstErr error
	for _, d := range drivers {
		if err := d.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
