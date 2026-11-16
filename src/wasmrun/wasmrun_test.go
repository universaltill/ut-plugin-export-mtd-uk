// Package wasmrun_test runs the REAL compiled plugin through a real wazero
// runtime — the same engine `universal-till` uses
// (internal/plugins/wasm_runtime.go) — with a minimal stand-in for the five
// host functions this plugin imports (`ut.log_write`, `ut.settings_get`,
// and, since ut-docs#1475's MTD VAT bridge, `ut.http_request`,
// `ut.storage_get`, `ut.storage_set`). Every import needs a stub here or
// the module will not instantiate at all.
//
// Why this exists: `src/main.go` is `GOOS=wasip1`-only, so neither
// `go test ./...` nor a host `go vet` ever sees its event dispatch. Same
// harness shape as `ut-plugin-tax-de`'s `src/wasmrun/`.
//
// WHAT THIS PROVES: the compiled module dispatches on the event's `type`,
// reads settings through the host ABI, and writes exactly the stdout JSON
// core parses (`internal/pages/data_api.go`'s exportResponse).
//
// WHAT THIS DOES NOT PROVE: `universal-till`'s real host-function
// implementations, or a real installed-plugin flow through the till UI.
package wasmrun_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Host ABI (ut-docs reference/plugin-host-functions.md, mirrored by every
// sibling plugin): data calls return the FULL length of the value — the
// guest retries with a bigger buffer if that exceeds its cap. Negative
// returns are host errors.
const (
	hostErrNotFound = -1
	hostErrDenied   = -2
)

// httpCall is one request the guest made, captured for assertions (same
// envelope as universal-till's hostHTTPRequest and ut-plugin-tax-de's stub).
type httpCall struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
}

// Body decodes the request body. The host ABI carries bodies base64-encoded
// in BOTH directions (`body_b64`).
func (c httpCall) Body() string {
	b, err := base64.StdEncoding.DecodeString(c.BodyB64)
	if err != nil {
		return ""
	}
	return string(b)
}

// stubHost is the fake till: settings, storage, a scripted HTTP layer and
// captured log lines. No real network call is ever made: respond answers
// every http_request, and a nil respond answers a transport failure.
type stubHost struct {
	mu       sync.Mutex
	settings map[string]string
	storage  map[string][]byte
	logs     []string
	reads    []string // setting keys the guest asked for, in order
	calls    []httpCall
	respond  func(c httpCall) (status int, body string, ok bool)
}

func writeOut(mem api.Memory, dstPtr, dstCap uint32, val []byte) int32 {
	if uint32(len(val)) <= dstCap {
		mem.Write(dstPtr, val)
	}
	return int32(len(val)) // full length either way, per the ABI
}

func readStr(mem api.Memory, ptr, n uint32) string {
	b, _ := mem.Read(ptr, n)
	return string(b)
}

func (h *stubHost) register(ctx context.Context, r wazero.Runtime) error {
	_, err := r.NewHostModuleBuilder("ut").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.logs = append(h.logs, readStr(m.Memory(), ptr, n))
	}).Export("log_write").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kPtr, kLen, dstPtr, dstCap uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		key := readStr(m.Memory(), kPtr, kLen)
		h.reads = append(h.reads, key)
		v, ok := h.settings[key]
		if !ok {
			return hostErrNotFound
		}
		return writeOut(m.Memory(), dstPtr, dstCap, []byte(v))
	}).Export("settings_get").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kPtr, kLen, dstPtr, dstCap uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		v, ok := h.storage[readStr(m.Memory(), kPtr, kLen)]
		if !ok {
			return hostErrNotFound
		}
		return writeOut(m.Memory(), dstPtr, dstCap, v)
	}).Export("storage_get").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kPtr, kLen, vPtr, vLen uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		k := readStr(m.Memory(), kPtr, kLen)
		b, _ := m.Memory().Read(vPtr, vLen)
		cp := make([]byte, len(b))
		copy(cp, b)
		if h.storage == nil {
			h.storage = map[string][]byte{}
		}
		h.storage[k] = cp
		return 0
	}).Export("storage_set").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, rPtr, rLen, dstPtr, dstCap uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		var req httpCall
		if err := json.Unmarshal([]byte(readStr(m.Memory(), rPtr, rLen)), &req); err != nil {
			return -4 // invalid
		}
		h.calls = append(h.calls, req)
		if h.respond == nil {
			return hostErrDenied
		}
		status, body, ok := h.respond(req)
		if !ok {
			return hostErrDenied
		}
		out, _ := json.Marshal(map[string]any{
			"status":   status,
			"body_b64": base64.StdEncoding.EncodeToString([]byte(body)),
		})
		return writeOut(m.Memory(), dstPtr, dstCap, out)
	}).Export("http_request").
		Instantiate(ctx)
	return err
}

// buildWasm compiles the CURRENT source to wasm, so this test can never
// pass against a stale committed artefact.
func buildWasm(t *testing.T) []byte {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// Register every guest source as an input of THIS test, so `go test`'s
	// result cache invalidates when they change. Load-bearing, not
	// defensive: the wasm is built by a subprocess, and the go command's
	// cache only tracks files the test process itself opens — without
	// these reads a cached PASS survives an edit that deletes the dispatch
	// (ut-plugin-tax-de hit exactly that, 2026-08-19).
	srcDir := filepath.Join(root, "src")
	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		_, readErr := os.ReadFile(path)
		return readErr
	})
	if err != nil {
		t.Fatalf("registering guest sources as cache inputs: %v", err)
	}

	out := filepath.Join(t.TempDir(), "plugin.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./src")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building wasm: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read wasm: %v", err)
	}
	return b
}

// run executes the compiled plugin with event JSON on stdin and returns
// stdout plus the host stub (for asserting what it did).
func run(t *testing.T, wasm []byte, h *stubHost, event string) (string, *stubHost) {
	t.Helper()
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		t.Fatalf("wasi: %v", err)
	}
	if err := h.register(ctx, r); err != nil {
		t.Fatalf("host module: %v", err)
	}

	var stdout, stderr strings.Builder
	cfg := wazero.NewModuleConfig().
		WithStdin(strings.NewReader(event)).
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithStartFunctions("_start")

	// The guest calls os.Exit(0); wazero surfaces that as a sys.ExitError
	// with code 0, which is a normal, successful finish for this plugin.
	if _, err := r.InstantiateWithConfig(ctx, wasm, cfg); err != nil {
		if !strings.Contains(err.Error(), "exit_code(0)") {
			t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
		}
	}
	return stdout.String(), h
}
