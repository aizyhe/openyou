package tool

import (
	"context"
	"errors"
	"testing"
)

func TestNewRegistry_Builtins(t *testing.T) {
	r := NewRegistry()
	names := r.Names()
	want := []string{"read", "webfetch", "websearch", "write"} // sorted
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("Names()[%d] = %q, want %q", i, names[i], n)
		}
	}

	// Built-in handlers default to the not-implemented stub.
	if _, err := r.Invoke(context.Background(), "read", nil); err == nil {
		t.Error("built-in read should return not-implemented error")
	}
}

func TestGet(t *testing.T) {
	r := NewRegistry()
	d, ok := r.Get("write")
	if !ok {
		t.Fatal("expected write tool to exist")
	}
	if d.Name != "write" || d.Description == "" {
		t.Errorf("bad def %+v", d)
	}
	if _, ok := r.Get("does-not-exist"); ok {
		t.Error("Get should report missing tool as not found")
	}
}

func TestRegister_OverwriteAndNilMap(t *testing.T) {
	// Register on a zero-value Registry lazily initializes the map.
	var r Registry
	r.Register(Def{Name: "custom", Description: "d"})
	if d, ok := r.Get("custom"); !ok || d.Description != "d" {
		t.Fatalf("Register did not store def: %+v ok=%v", d, ok)
	}

	// Overwrite replaces the existing def.
	r.Register(Def{Name: "custom", Description: "updated"})
	if d, _ := r.Get("custom"); d.Description != "updated" {
		t.Errorf("Register overwrite failed, got %q", d.Description)
	}
}

func TestSetHandlerAndInvoke(t *testing.T) {
	r := NewRegistry()

	called := false
	ok := r.SetHandler("read", func(ctx context.Context, args map[string]any) (string, error) {
		called = true
		return "content of " + args["path"].(string), nil
	})
	if !ok {
		t.Fatal("SetHandler should succeed for a registered tool")
	}

	out, err := r.Invoke(context.Background(), "read", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !called || out != "content of a.txt" {
		t.Errorf("handler not invoked correctly: called=%v out=%q", called, out)
	}
}

func TestSetHandler_UnknownTool(t *testing.T) {
	r := NewRegistry()
	if r.SetHandler("nope", func(context.Context, map[string]any) (string, error) { return "", nil }) {
		t.Error("SetHandler should return false for unknown tool")
	}
}

func TestInvoke_UnknownTool(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Invoke(context.Background(), "nope", nil); err == nil {
		t.Error("Invoke should error on unknown tool")
	}
}

func TestInvoke_NilHandler(t *testing.T) {
	r := NewRegistry()
	// Register a def with an explicitly nil handler.
	r.Register(Def{Name: "nilh", Description: "d"})
	if _, err := r.Invoke(context.Background(), "nilh", nil); err == nil {
		t.Error("Invoke should error when handler is nil")
	}
}

func TestInvoke_HandlerError(t *testing.T) {
	r := NewRegistry()
	sentinel := errors.New("boom")
	r.SetHandler("write", func(context.Context, map[string]any) (string, error) {
		return "", sentinel
	})
	_, err := r.Invoke(context.Background(), "write", nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("Invoke should propagate handler error, got %v", err)
	}
}

func TestNames_Empty(t *testing.T) {
	var r Registry
	if names := r.Names(); len(names) != 0 {
		t.Errorf("empty registry Names() = %v, want empty", names)
	}
}
