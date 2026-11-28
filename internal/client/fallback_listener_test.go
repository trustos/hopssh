package client

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// When Nebula could not start, the agent served its whole API (exec, shell,
// upload) on :41820 of every interface, outside the mesh and its firewall,
// with the bearer token as the only gate. Nothing needs that listener:
// renewal and heartbeats are outbound calls, and the dashboard reaches the
// agent only through Nebula. On Linux it could also keep another network's
// mesh listener off its address (kernel TUN binds <mesh IP>:41820, the
// fallback held *:41820). A network whose Nebula is down serves nothing; its
// listener comes up on the mesh once Nebula does.

type hookFunc func(name, token string) http.Handler

func (f hookFunc) BuildHandler(name, token string) http.Handler { return f(name, token) }

func newServingClient(t *testing.T) *Client {
	t.Helper()
	c, _ := newRecoveryTestClient(t, time.Hour)
	c.httpHook = hookFunc(func(string, string) http.Handler { return http.NotFoundHandler() })
	return c
}

// agentServer is the HTTP server the client keeps for the network, or nil.
func agentServer(c *Client, name string) *http.Server {
	c.servers.mu.Lock()
	entry := c.servers.servers[name]
	c.servers.mu.Unlock()
	if entry == nil {
		return nil
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.srv
}

func startWithoutNebula(t *testing.T, c *Client, inst *meshInstance) {
	t.Helper()
	if err := c.startInstance(c.runCtx, inst); err != nil {
		t.Fatalf("startInstance: %v (a failed Nebula start is soft)", err)
	}
	t.Cleanup(func() {
		inst.close()
		c.servers.shutdownInstance(inst.name())
	})
}

func TestStartInstance_NebulaFails_ServesNothing(t *testing.T) {
	c := newServingClient(t)
	inst := newStartableInstance(t)
	substituteStartMesh(t, func(string, string) (meshService, error) {
		return nil, errors.New("address already in use")
	})
	startWithoutNebula(t, c, inst)
	if agentServer(c, inst.name()) != nil {
		t.Error("the agent API is served on the OS stack, outside the mesh, while Nebula is down")
	}
}

func TestStartInstance_NoConfig_ServesNothing(t *testing.T) {
	c := newServingClient(t)
	inst := newStartableInstance(t)
	if err := os.Remove(filepath.Join(inst.dir(), "nebula.yaml")); err != nil {
		t.Fatal(err)
	}
	substituteStartMesh(t, func(string, string) (meshService, error) {
		t.Error("Nebula started without a config")
		return nil, errors.New("no config")
	})
	startWithoutNebula(t, c, inst)
	if agentServer(c, inst.name()) != nil {
		t.Error("the agent API is served on the OS stack, outside the mesh, with no Nebula config")
	}
}

// TestColdStart_AfterFailedStart_ServesOnMesh: the recovery path. Nebula
// fails at start, a renewal later starts it (reloadNebula's cold start), and
// the agent API must then be served on the mesh listener.
func TestColdStart_AfterFailedStart_ServesOnMesh(t *testing.T) {
	c := newServingClient(t)
	inst := newStartableInstance(t)
	substituteStartMesh(t, func(string, string) (meshService, error) {
		return nil, errors.New("address already in use")
	})
	startWithoutNebula(t, c, inst)

	svc := &listeningFakeSvc{}
	substituteStartMesh(t, func(string, string) (meshService, error) { return svc, nil })
	reloadNebula(inst)

	if inst.currentSvc() == nil {
		t.Fatal("cold start did not install Nebula")
	}
	if agentServer(c, inst.name()) == nil {
		t.Error("after Nebula came up the agent API is not served on the mesh")
	}
}
