package privacy

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Pseudonymizer replaces real person names with pseudonyms before data leaves
// the device, so even compromised LLM provider logs never expose real names.
type Pseudonymizer struct {
	mu      sync.RWMutex
	nameMap map[string]string // real name → pseudonym
	revMap  map[string]string // pseudonym → real name
	counter int
	enabled bool // balanced mode on; when false Mask/Unmask are pass-through
}

// pseudonymFor returns the sentinel 〔代号N〕 for the i-th real name. The
// non-linguistic 〔〕 brackets keep Unmask from falsely rewriting model output
// into real names; New and UpdateNames share this helper so schemes never diverge.
func pseudonymFor(i int) string {
	return "〔代号" + strconv.Itoa(i+1) + "〕"
}

func buildMaps(realNames []string) (nameMap, revMap map[string]string) {
	nameMap = make(map[string]string, len(realNames))
	revMap = make(map[string]string, len(realNames))
	for i, name := range realNames {
		// Skip empty names — replacing "" would insert the pseudonym between
		// every rune, corrupting the whole prompt.
		if name == "" {
			continue
		}
		pseudo := pseudonymFor(i)
		nameMap[name] = pseudo
		revMap[pseudo] = name
	}
	return nameMap, revMap
}

func New(realNames []string) *Pseudonymizer {
	nameMap, revMap := buildMaps(realNames)
	return &Pseudonymizer{
		nameMap: nameMap,
		revMap:  revMap,
		enabled: true,
	}
}

// SetEnabled: disabled (trust mode) makes Mask/Unmask pass-through.
func (p *Pseudonymizer) SetEnabled(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enabled = on
}

func (p *Pseudonymizer) Enabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.enabled
}

// MaskWindow returns a byte count that fully contains any single codename or
// real name, so a streaming unmasker never emits a half-resolved one.
func (p *Pseudonymizer) MaskWindow() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	max := 16 // floor for "Engineer A1"-style codenames
	for real, pseudo := range p.nameMap {
		if len(real) > max {
			max = len(real)
		}
		if len(pseudo) > max {
			max = len(pseudo)
		}
	}
	return max
}

// keysByLenDesc sorts m's keys longest-first (deterministic), so a shorter name
// never mangles a longer one that contains it.
func keysByLenDesc(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if len(keys[a]) != len(keys[b]) {
			return len(keys[a]) > len(keys[b])
		}
		return keys[a] < keys[b]
	})
	return keys
}

func (p *Pseudonymizer) Mask(text string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.enabled {
		return text
	}
	result := text
	for _, real := range keysByLenDesc(p.nameMap) {
		result = strings.ReplaceAll(result, real, p.nameMap[real])
	}
	return result
}

func (p *Pseudonymizer) Unmask(text string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.enabled {
		return text
	}
	result := text
	for _, pseudo := range keysByLenDesc(p.revMap) {
		result = strings.ReplaceAll(result, pseudo, p.revMap[pseudo])
	}
	return result
}

func (p *Pseudonymizer) UpdateNames(realNames []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nameMap, p.revMap = buildMaps(realNames)
}

// frozenMasker is an immutable copy of the mapping; pseudonyms are positional,
// so a request must mask and unmask with the same snapshot or mid-stream
// reseeds would unmask to the wrong people.
type frozenMasker struct {
	nameMap map[string]string
	revMap  map[string]string
	window  int
	enabled bool
}

func (f *frozenMasker) Mask(text string) string {
	if !f.enabled {
		return text
	}
	result := text
	for _, real := range keysByLenDesc(f.nameMap) {
		result = strings.ReplaceAll(result, real, f.nameMap[real])
	}
	return result
}

func (f *frozenMasker) Unmask(text string) string {
	if !f.enabled {
		return text
	}
	result := text
	for _, pseudo := range keysByLenDesc(f.revMap) {
		result = strings.ReplaceAll(result, pseudo, f.revMap[pseudo])
	}
	return result
}

func (f *frozenMasker) MaskWindow() int { return f.window }

// Snapshot returns an immutable masker for the whole of one LLM request (see frozenMasker).
func (p *Pseudonymizer) Snapshot() *frozenMasker {
	p.mu.RLock()
	defer p.mu.RUnlock()
	nameMap := make(map[string]string, len(p.nameMap))
	for k, v := range p.nameMap {
		nameMap[k] = v
	}
	revMap := make(map[string]string, len(p.revMap))
	for k, v := range p.revMap {
		revMap[k] = v
	}
	max := 16
	for real, pseudo := range nameMap {
		if len(real) > max {
			max = len(real)
		}
		if len(pseudo) > max {
			max = len(pseudo)
		}
	}
	return &frozenMasker{nameMap: nameMap, revMap: revMap, window: max, enabled: p.enabled}
}
