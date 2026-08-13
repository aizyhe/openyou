package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

type LoopDetector struct {
	window    []string // recent call signatures (ring, newest last)
	size      int      // window capacity
	repeatCap int      // same signature this many times in window → loop
}

func NewLoopDetector() *LoopDetector {
	return &LoopDetector{size: 6, repeatCap: 3}
}

func Signature(toolName string, args map[string]any) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0})
	h.Write([]byte(canonicalArgs(args)))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (d *LoopDetector) Record(sig string) (looping bool, reason string) {
	d.window = append(d.window, sig)
	if len(d.window) > d.size {
		d.window = d.window[len(d.window)-d.size:]
	}

	count := 0
	for _, s := range d.window {
		if s == sig {
			count++
		}
	}
	if count >= d.repeatCap {
		return true, "repeated identical tool call"
	}

	if len(d.window) >= 4 {
		w := d.window[len(d.window)-4:]
		if w[0] != w[1] && w[0] == w[2] && w[1] == w[3] {
			return true, "ping-pong between two tool calls"
		}
	}
	return false, ""
}

func canonicalArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	for _, k := range keys {
		b = append(b, k...)
		b = append(b, '=')
		b = append(b, []byte(fmt.Sprint(args[k]))...)
		b = append(b, ';')
	}
	return string(b)
}
