// Package tool is the registry of callable tools the chat/agent loop can invoke.
package tool

import (
	"context"
	"fmt"
	"sort"
)

type Handler func(ctx context.Context, args map[string]any) (string, error)

type Def struct {
	Name        string
	Description string
	handler     Handler
}

type Registry struct {
	tools map[string]*Def
}

// NewRegistry: handlers default to a "not yet implemented" stub so the registry
// is safe to wire before individual tools are fully built out.
func NewRegistry() *Registry {
	r := &Registry{tools: make(map[string]*Def)}
	for _, name := range []struct{ n, d string }{
		{"read", "Read a file from the context tree or workspace"},
		{"write", "Write or overwrite a file"},
		{"websearch", "Search the web for external information"},
		{"webfetch", "Fetch and extract the contents of a URL"},
	} {
		name := name
		r.Register(Def{
			Name:        name.n,
			Description: name.d,
			handler: func(ctx context.Context, args map[string]any) (string, error) {
				return "", fmt.Errorf("tool %q not yet implemented", name.n)
			},
		})
	}
	return r
}

func (r *Registry) Register(d Def) {
	if r.tools == nil {
		r.tools = make(map[string]*Def)
	}
	cp := d
	r.tools[d.Name] = &cp
}

func (r *Registry) SetHandler(name string, h Handler) bool {
	d, ok := r.tools[name]
	if !ok {
		return false
	}
	d.handler = h
	return true
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Get(name string) (*Def, bool) {
	d, ok := r.tools[name]
	return d, ok
}

func (r *Registry) Invoke(ctx context.Context, name string, args map[string]any) (string, error) {
	d, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if d.handler == nil {
		return "", fmt.Errorf("tool %q has no handler", name)
	}
	return d.handler(ctx, args)
}
