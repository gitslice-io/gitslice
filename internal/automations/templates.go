package automations

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	gspaths "gitslice.io/gitslice/internal/paths"
	"gitslice.io/gitslice/internal/storage"
)

// UsesRef is a parsed action.uses reference:
// /<account>[/<dir>...]/<name>[@<tag>|@sha256:<hex>]
type UsesRef struct {
	Raw     string
	Account string
	Folder  string // canonical folder holding .gitslice/automations.yaml
	Name    string // local automation name in that file
	ID      string // "<rel>/<name>" relative to account root (or "<name>" at root)
	Pin     string // "", tag name, or "sha256:<hex>"
	PinKind string // "", "tag", or "commit"
}

// ResolvedTemplateTree identifies the concrete commit and root tree of a
// resolved template reference.
type ResolvedTemplateTree struct {
	CommitID      string
	RootTreeID    string
	ResolvedRef   string
	AvailableTags []string
}

// TemplateLookup resolves a UsesRef to a commit/tree and checks caller access.
type TemplateLookup interface {
	ResolveTemplateTree(ctx context.Context, callerAccount, callerCommitID, callerRootTreeID string, ref UsesRef) (*ResolvedTemplateTree, error)
}

// LoadedTemplate is a parsed on:[call] automation template loaded from a tree.
type LoadedTemplate struct {
	Ref           UsesRef
	Automation    Automation
	CommitID      string
	RootTreeID    string
	ResolvedRef   string
	AvailableTags []string
}

// ResolvedCall is a concrete executable automation produced by binding a
// caller's action.uses (and capability ceilings) against a LoadedTemplate.
type ResolvedCall struct {
	Automation       Automation
	Inputs           map[string]string
	TemplateID       string
	TemplateFolder   string
	TemplateCommitID string
	TemplateTreeID   string
}

var (
	inputExprPattern      = regexp.MustCompile(`\$\{\{\s*inputs\.([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}`)
	stepOutputExprPattern = regexp.MustCompile(`\$\{\{\s*steps\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}`)
	commitPinPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{8,64}$`)
	tagPinPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	outputKeyPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
)

// MaxRunOutputs bounds the number and total size of named outputs per run.
const (
	MaxRunOutputs      = 32
	MaxRunOutputBytes  = 4 << 10
	MaxRunOutputsTotal = 16 << 10
)

// ValidOutputKey reports whether key is a valid output name.
func ValidOutputKey(key string) bool {
	return outputKeyPattern.MatchString(strings.TrimSpace(key))
}

// ParseUsesRef parses /<account>[/<dir>...]/<name>[@<tag>|@sha256:<hex>].
func ParseUsesRef(raw string) (UsesRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return UsesRef{}, fmt.Errorf("uses reference is empty")
	}
	pathPart := trimmed
	pin := ""
	if at := strings.LastIndex(trimmed, "@"); at >= 0 {
		pathPart = trimmed[:at]
		pin = strings.TrimSpace(trimmed[at+1:])
		if pin == "" {
			return UsesRef{}, fmt.Errorf("uses %q: empty version pin after @", raw)
		}
	}
	if !strings.HasPrefix(pathPart, "/") {
		return UsesRef{}, fmt.Errorf("uses %q: must start with /<account>/...", raw)
	}
	if strings.Contains(pathPart, "..") {
		return UsesRef{}, fmt.Errorf("uses %q: must not contain ..", raw)
	}
	clean, err := gspaths.CanonicalPrefix(pathPart)
	if err != nil {
		return UsesRef{}, fmt.Errorf("uses %q: %v", raw, err)
	}
	segs := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(segs) < 2 {
		return UsesRef{}, fmt.Errorf("uses %q: expected /<account>[/<folder>]/<name>", raw)
	}
	account := segs[0]
	name := segs[len(segs)-1]
	if !localNamePattern.MatchString(name) {
		return UsesRef{}, fmt.Errorf("uses %q: invalid automation name %q", raw, name)
	}
	folder := "/" + strings.Join(segs[:len(segs)-1], "/")
	id := name
	if len(segs) > 2 {
		id = strings.Join(segs[1:], "/")
	}
	pinKind := ""
	if pin != "" {
		if strings.HasPrefix(pin, "sha256:") {
			if !commitPinPattern.MatchString(pin) {
				return UsesRef{}, fmt.Errorf("uses %q: invalid commit pin %q", raw, pin)
			}
			pinKind = "commit"
		} else {
			if !tagPinPattern.MatchString(pin) || strings.Contains(pin, "..") {
				return UsesRef{}, fmt.Errorf("uses %q: invalid tag pin %q", raw, pin)
			}
			pinKind = "tag"
		}
	}
	return UsesRef{
		Raw:     trimmed,
		Account: account,
		Folder:  folder,
		Name:    name,
		ID:      id,
		Pin:     pin,
		PinKind: pinKind,
	}, nil
}

func parseSecretSpecs(raw rawSecrets) ([]string, map[string]string, error) {
	if len(raw.Bindings) > 0 {
		slots := make([]string, 0, len(raw.Bindings))
		for slot := range raw.Bindings {
			slots = append(slots, slot)
		}
		sort.Strings(slots)
		var secrets []string
		bindings := make(map[string]string, len(raw.Bindings))
		for _, slot := range slots {
			callerSecret := strings.TrimSpace(raw.Bindings[slot])
			if !secretNamePattern.MatchString(slot) {
				return nil, nil, fmt.Errorf("secret slot %q must match ^[A-Z_][A-Z0-9_]*$", slot)
			}
			if !secretNamePattern.MatchString(callerSecret) {
				return nil, nil, fmt.Errorf("secret name %q must match ^[A-Z_][A-Z0-9_]*$", callerSecret)
			}
			bindings[slot] = callerSecret
			secrets = appendUnique(secrets, callerSecret)
		}
		return secrets, bindings, nil
	}
	var secrets []string
	for _, name := range raw.List {
		if !secretNamePattern.MatchString(name) {
			return nil, nil, fmt.Errorf("secret name %q must match ^[A-Z_][A-Z0-9_]*$", name)
		}
		secrets = appendUnique(secrets, name)
	}
	return secrets, nil, nil
}

func parseUsesAction(rawRef string, rawWith map[string]any) (*UsesAction, error) {
	ref, err := ParseUsesRef(rawRef)
	if err != nil {
		return nil, err
	}
	with := map[string]string{}
	if len(rawWith) > 0 {
		keys := make([]string, 0, len(rawWith))
		for k := range rawWith {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := strings.TrimSpace(k)
			if !inputNamePattern.MatchString(name) {
				return nil, fmt.Errorf("action.with key %q must match ^[a-zA-Z_][a-zA-Z0-9_-]*$", k)
			}
			val := fmt.Sprint(rawWith[k])
			if rawWith[k] == nil {
				val = ""
			}
			if len(val) > MaxInputValueBytes {
				return nil, fmt.Errorf("action.with.%s exceeds %d bytes", name, MaxInputValueBytes)
			}
			with[name] = val
		}
	}
	return &UsesAction{Ref: ref.Raw, With: with}, nil
}

func parseSteps(rawSteps []rawStep, parent Automation) ([]StepSpec, error) {
	if len(rawSteps) == 0 {
		return nil, fmt.Errorf("action.steps must have at least one step")
	}
	if len(rawSteps) > MaxSteps {
		return nil, fmt.Errorf("action.steps has %d steps (at most %d)", len(rawSteps), MaxSteps)
	}
	seenIDs := map[string]bool{}
	out := make([]StepSpec, 0, len(rawSteps))
	for i, rs := range rawSteps {
		id := strings.TrimSpace(rs.ID)
		if id == "" {
			id = fmt.Sprintf("step-%d", i+1)
		}
		if !localNamePattern.MatchString(id) {
			return nil, fmt.Errorf("step %d id %q: names use letters, digits, _ and -", i+1, id)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("duplicate step id %q", id)
		}
		seenIDs[id] = true

		cond := strings.TrimSpace(rs.If)
		if cond == "" {
			cond = StepIfSuccess
		}
		switch cond {
		case StepIfSuccess, StepIfFailure, StepIfAlways:
		default:
			return nil, fmt.Errorf("step %s: if %q (use success, failure or always)", id, cond)
		}

		secrets, bindings, err := parseSecretSpecs(rs.Secrets)
		if err != nil {
			return nil, fmt.Errorf("step %s: %v", id, err)
		}
		// Step secrets must be a subset of parent secrets when specified;
		// when omitted on inline actions, inherit parent secrets.
		if len(secrets) > 0 {
			for _, sName := range secrets {
				if !contains(parent.Secrets, sName) {
					return nil, fmt.Errorf("step %s: secret %s must also be listed in the automation's secrets", id, sName)
				}
			}
		} else {
			secrets = append([]string(nil), parent.Secrets...)
			if len(parent.SecretBindings) > 0 {
				bindings = cloneStringMap(parent.SecretBindings)
			}
		}

		var reveal []string
		if len(rs.Reveal) > 0 {
			for _, rName := range rs.Reveal {
				if rName != RunTokenSecret && !contains(parent.Reveal, rName) {
					return nil, fmt.Errorf("step %s: reveal %s must also be listed in the automation's reveal", id, rName)
				}
				reveal = appendUnique(reveal, rName)
			}
		} else {
			reveal = append([]string(nil), parent.Reveal...)
		}

		var network []string
		if len(rs.Network) > 0 {
			for _, host := range rs.Network {
				host = strings.ToLower(strings.TrimSpace(host))
				if !validNetworkHostPattern(host) {
					return nil, fmt.Errorf("step %s: network host %q is invalid", id, host)
				}
				if len(parent.Network) > 0 && !hostCoveredBy(host, parent.Network) {
					return nil, fmt.Errorf("step %s: network host %s is not allowed by the automation's network", id, host)
				}
				network = appendUnique(network, host)
			}
		} else {
			network = append([]string(nil), parent.Network...)
		}

		egress := strings.TrimSpace(rs.Egress)
		if egress == "" {
			egress = parent.Egress
		}
		if egress == EgressDirect && parent.Egress != EgressDirect {
			return nil, fmt.Errorf("step %s: egress direct requires egress: direct on the automation", id)
		}

		stepAuto := Automation{
			Secrets:        secrets,
			SecretBindings: bindings,
			Reveal:         reveal,
			Network:        network,
			Egress:         egress,
		}

		kinds := 0
		for _, set := range []bool{rs.HTTP != nil, rs.Worker != "", rs.Check != nil, rs.Bash != nil, strings.TrimSpace(rs.Uses) != ""} {
			if set {
				kinds++
			}
		}
		if len(rs.With) > 0 && strings.TrimSpace(rs.Uses) == "" {
			return nil, fmt.Errorf("step %s: with requires uses", id)
		}
		if kinds != 1 {
			return nil, fmt.Errorf("step %s: must set exactly one of uses, bash, worker, http or check", id)
		}
		var act Action
		switch {
		case strings.TrimSpace(rs.Uses) != "":
			uses, err := parseUsesAction(rs.Uses, rs.With)
			if err != nil {
				return nil, fmt.Errorf("step %s: %v", id, err)
			}
			act = Action{Kind: ActionUses, Uses: uses}
		case rs.Bash != nil:
			bash, resolvedEgress, err := parseBashAction(*rs.Bash, stepAuto)
			if err != nil {
				return nil, fmt.Errorf("step %s: %v", id, err)
			}
			egress = resolvedEgress
			act = Action{Kind: ActionBash, Bash: bash}
		case rs.Worker != "":
			script, err := cleanScriptPath(rs.Worker)
			if err != nil {
				return nil, fmt.Errorf("step %s: %v", id, err)
			}
			act = Action{Kind: ActionWorker, Worker: script}
		case rs.HTTP != nil:
			allowedHTTP := append([]string(nil), secrets...)
			for slot := range bindings {
				allowedHTTP = appendUnique(allowedHTTP, slot)
			}
			httpAct, err := parseHTTP(*rs.HTTP, allowedHTTP)
			if err != nil {
				return nil, fmt.Errorf("step %s: %v", id, err)
			}
			if len(bindings) > 0 {
				httpAct.URL = remapSecretRefs(httpAct.URL, bindings)
				for k, v := range httpAct.Headers {
					httpAct.Headers[k] = remapSecretRefs(v, bindings)
				}
				if httpAct.Sign != "" && bindings[httpAct.Sign] != "" {
					httpAct.Sign = bindings[httpAct.Sign]
				}
			}
			act = Action{Kind: ActionHTTP, HTTP: httpAct}
		case rs.Check != nil:
			if len(rs.Check) == 0 {
				return nil, fmt.Errorf("step %s: check names no checks", id)
			}
			var checks []string
			for _, c := range rs.Check {
				if !localNamePattern.MatchString(c) {
					return nil, fmt.Errorf("step %s: check %q invalid", id, c)
				}
				checks = appendUnique(checks, c)
			}
			act = Action{Kind: ActionCheck, Check: checks}
		}

		var timeout time.Duration
		timeoutExplicit := false
		if strings.TrimSpace(rs.Timeout) != "" {
			d, err := parseDuration(rs.Timeout)
			if err != nil {
				return nil, fmt.Errorf("step %s: timeout: %v", id, err)
			}
			maxT := MaxTimeout
			if act.Kind == ActionBash {
				maxT = MaxBashTimeout
			}
			if d <= 0 || d > maxT {
				return nil, fmt.Errorf("step %s: timeout must be between 1s and %s", id, maxT)
			}
			timeout = d
			timeoutExplicit = true
		}

		out = append(out, StepSpec{
			ID:              id,
			Name:            strings.TrimSpace(rs.Name),
			If:              cond,
			Secrets:         secrets,
			SecretBindings:  bindings,
			Reveal:          reveal,
			Network:         network,
			Egress:          egress,
			Timeout:         timeout,
			TimeoutExplicit: timeoutExplicit,
			Action:          act,
		})
	}
	return out, nil
}

// ResolveTemplate loads and validates an on:[call] automation template from its
// pinned commit/tag (or caller's live tree for same-account unpinned refs).
func ResolveTemplate(ctx context.Context, tree TreeReader, lookup TemplateLookup, callerAccount, callerCommitID, callerRootTreeID string, ref UsesRef) (*LoadedTemplate, error) {
	if ref.Account != callerAccount && ref.Pin == "" {
		return nil, fmt.Errorf("%w: cross-account uses %s must be pinned with @<tag> or @sha256:<commit>", storage.ErrInvalid, ref.Raw)
	}
	var resolved *ResolvedTemplateTree
	if lookup != nil {
		var err error
		resolved, err = lookup.ResolveTemplateTree(ctx, callerAccount, callerCommitID, callerRootTreeID, ref)
		if err != nil {
			return nil, err
		}
	} else {
		if ref.Account != callerAccount || ref.Pin != "" {
			return nil, fmt.Errorf("%w: template resolution for %s is not configured", storage.ErrInvalid, ref.Raw)
		}
		resolved = &ResolvedTemplateTree{
			CommitID:    callerCommitID,
			RootTreeID:  callerRootTreeID,
			ResolvedRef: "live",
		}
	}
	if resolved == nil || resolved.RootTreeID == "" {
		return nil, fmt.Errorf("%w: template %s has no tree", storage.ErrNotFound, ref.Raw)
	}
	path := ref.Folder + "/" + FileName
	data, err := tree.ReadFile(ctx, resolved.RootTreeID, path)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: template %s: %s not found at %s", storage.ErrNotFound, ref.Raw, path, resolved.ResolvedRef)
	}
	if err != nil {
		return nil, err
	}
	file, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: template %s (%s): %v", storage.ErrInvalid, ref.Raw, path, err)
	}
	tmpl, ok := file.Automations[ref.Name]
	if !ok {
		return nil, fmt.Errorf("%w: template %s: automation %q not found in %s", storage.ErrNotFound, ref.Raw, ref.Name, path)
	}
	if !contains(tmpl.On, EventCall) {
		return nil, fmt.Errorf("%w: template %s is not reusable (add call to its on: list)", storage.ErrInvalid, ref.Raw)
	}
	if tmpl.Action.Kind == ActionUses || tmpl.Action.Kind == ActionSteps {
		return nil, fmt.Errorf("%w: template %s cannot nest uses/steps actions", storage.ErrInvalid, ref.Raw)
	}
	return &LoadedTemplate{
		Ref:           ref,
		Automation:    tmpl,
		CommitID:      resolved.CommitID,
		RootTreeID:    resolved.RootTreeID,
		ResolvedRef:   resolved.ResolvedRef,
		AvailableTags: resolved.AvailableTags,
	}, nil
}

// EffectiveInputsForUses merges caller-declared inputs with unbound template
// inputs so CLI and Web UI prompt for everything the template still needs.
func EffectiveInputsForUses(caller Automation, tmpl Automation) map[string]InputSpec {
	if len(caller.Inputs) == 0 && len(tmpl.Inputs) == 0 {
		return nil
	}
	out := make(map[string]InputSpec, len(caller.Inputs)+len(tmpl.Inputs))
	for k, v := range caller.Inputs {
		out[k] = v
	}
	var fixed map[string]string
	if caller.Action.Uses != nil {
		fixed = caller.Action.Uses.With
	}
	for k, v := range tmpl.Inputs {
		if _, alreadySet := fixed[k]; alreadySet {
			continue
		}
		if _, overridden := out[k]; overridden {
			continue
		}
		out[k] = v
	}
	return out
}

// BindTemplate checks caller-bounded capability ceilings (secrets, reveal,
// network, egress, timeout), interpolates action.with + runtime inputs, and
// produces a concrete Automation ready to execute.
func BindTemplate(caller Automation, callerAccount string, loaded *LoadedTemplate, runtimeInputs map[string]string, stepOutputs map[string]map[string]string) (ResolvedCall, error) {
	tmpl := loaded.Automation
	sameAccount := loaded.Ref.Account == callerAccount

	// 1. Secrets: map every template slot to an explicitly granted caller secret.
	effectiveSecrets := make([]string, 0, len(tmpl.Secrets))
	effectiveBindings := make(map[string]string, len(tmpl.Secrets))
	for _, slot := range tmpl.Secrets {
		callerSecret := ""
		if caller.SecretBindings != nil {
			callerSecret = caller.SecretBindings[slot]
		}
		if callerSecret == "" && contains(caller.Secrets, slot) {
			callerSecret = slot
		}
		if callerSecret == "" {
			return ResolvedCall{}, fmt.Errorf("%w: template %s requires secret %s; bind it in secrets:", storage.ErrInvalid, loaded.Ref.Raw, slot)
		}
		effectiveBindings[slot] = callerSecret
		effectiveSecrets = appendUnique(effectiveSecrets, callerSecret)
	}

	// 2. Reveal ceiling: caller must explicitly allow revealing any secret the template reveals.
	effectiveReveal := make([]string, 0, len(tmpl.Reveal))
	for _, slot := range tmpl.Reveal {
		callerSecret := effectiveBindings[slot]
		if slot == RunTokenSecret {
			if !contains(caller.Reveal, RunTokenSecret) {
				return ResolvedCall{}, fmt.Errorf("%w: template %s reveals %s; caller must explicitly allow it in reveal:", storage.ErrInvalid, loaded.Ref.Raw, RunTokenSecret)
			}
			effectiveReveal = appendUnique(effectiveReveal, RunTokenSecret)
			continue
		}
		if !contains(caller.Reveal, slot) && (callerSecret == "" || !contains(caller.Reveal, callerSecret)) {
			return ResolvedCall{}, fmt.Errorf("%w: template %s reveals secret %s; caller must explicitly allow it in reveal:", storage.ErrInvalid, loaded.Ref.Raw, slot)
		}
		effectiveReveal = appendUnique(effectiveReveal, slot)
	}

	// 3. Network ceiling: cross-account callers must explicitly allow every host
	// the template contacts; same-account callers may omit network: to inherit.
	var effectiveNetwork []string
	if len(tmpl.Network) > 0 {
		if sameAccount && len(caller.Network) == 0 {
			effectiveNetwork = append([]string(nil), tmpl.Network...)
		} else {
			for _, host := range tmpl.Network {
				if !hostCoveredBy(host, caller.Network) {
					return ResolvedCall{}, fmt.Errorf("%w: template %s requires network host %s; allow it in network:", storage.ErrInvalid, loaded.Ref.Raw, host)
				}
				effectiveNetwork = appendUnique(effectiveNetwork, host)
			}
		}
	}

	// 4. Egress ceiling: template cannot escalate to egress: direct without caller opt-in.
	effectiveEgress := tmpl.Egress
	if effectiveEgress == EgressDirect && caller.Egress != EgressDirect {
		return ResolvedCall{}, fmt.Errorf("%w: template %s requires egress: direct; caller must explicitly set egress: direct", storage.ErrInvalid, loaded.Ref.Raw)
	}

	// 5. Timeout ceiling: cap by caller timeout when explicitly specified.
	effectiveTimeout := tmpl.Timeout
	if caller.TimeoutExplicit && caller.Timeout > 0 && (effectiveTimeout == 0 || caller.Timeout < effectiveTimeout) {
		effectiveTimeout = caller.Timeout
	}

	// 6. Inputs: interpolate action.with (${{ inputs.X }} and ${{ steps.ID.outputs.K }}),
	// merge unbound runtime inputs, and validate against template InputSpecs.
	mergedInputs := map[string]string{}
	if caller.Action.Uses != nil {
		for k, v := range caller.Action.Uses.With {
			mergedInputs[k] = InterpolateExpressions(v, runtimeInputs, stepOutputs)
		}
	}
	for k, v := range runtimeInputs {
		if _, set := mergedInputs[k]; !set {
			mergedInputs[k] = v
		}
	}
	validatedInputs, err := ValidateInputs(tmpl.Inputs, mergedInputs)
	if err != nil {
		return ResolvedCall{}, fmt.Errorf("%w: template %s: %v", storage.ErrInvalid, loaded.Ref.Raw, err)
	}

	// 7. Build concrete executable Automation.
	resolved := tmpl
	resolved.On = caller.On
	resolved.Manual = caller.Manual
	resolved.Concurrency = caller.Concurrency
	resolved.ConcurrencyGroup = caller.ConcurrencyGroup
	resolved.Secrets = effectiveSecrets
	resolved.SecretBindings = effectiveBindings
	resolved.Reveal = effectiveReveal
	resolved.Network = effectiveNetwork
	resolved.Egress = effectiveEgress
	resolved.Timeout = effectiveTimeout
	if caller.Retry > 0 {
		resolved.Retry = caller.Retry
	}
	if resolved.Action.Kind == ActionHTTP && resolved.Action.HTTP != nil {
		// Remap ${{ secrets.SLOT }} in template HTTP URL/headers to caller secret names.
		httpCopy := *resolved.Action.HTTP
		httpCopy.URL = remapSecretRefs(httpCopy.URL, effectiveBindings)
		if len(httpCopy.Headers) > 0 {
			headers := make(map[string]string, len(httpCopy.Headers))
			for k, v := range httpCopy.Headers {
				headers[k] = remapSecretRefs(v, effectiveBindings)
			}
			httpCopy.Headers = headers
		}
		if httpCopy.Sign != "" && effectiveBindings[httpCopy.Sign] != "" {
			httpCopy.Sign = effectiveBindings[httpCopy.Sign]
		}
		resolved.Action.HTTP = &httpCopy
	}

	return ResolvedCall{
		Automation:       resolved,
		Inputs:           validatedInputs,
		TemplateID:       "/" + loaded.Ref.Account + "/" + loaded.Ref.ID,
		TemplateFolder:   loaded.Ref.Folder,
		TemplateCommitID: loaded.CommitID,
		TemplateTreeID:   loaded.RootTreeID,
	}, nil
}

// InterpolateExpressions replaces ${{ inputs.<name> }} and
// ${{ steps.<id>.outputs.<key> }} expressions in text.
func InterpolateExpressions(text string, inputs map[string]string, stepOutputs map[string]map[string]string) string {
	if !strings.Contains(text, "${{") {
		return text
	}
	out := inputExprPattern.ReplaceAllStringFunc(text, func(m string) string {
		sub := inputExprPattern.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		if v, ok := inputs[sub[1]]; ok {
			return v
		}
		return ""
	})
	out = stepOutputExprPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := stepOutputExprPattern.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		if stepMap := stepOutputs[sub[1]]; stepMap != nil {
			if v, ok := stepMap[sub[2]]; ok {
				return v
			}
		}
		return ""
	})
	return out
}

func remapSecretRefs(text string, bindings map[string]string) string {
	if len(bindings) == 0 || !strings.Contains(text, "${{") {
		return text
	}
	return SecretRef.ReplaceAllStringFunc(text, func(m string) string {
		sub := SecretRef.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		if mapped := bindings[sub[1]]; mapped != "" {
			return "${{ secrets." + mapped + " }}"
		}
		return m
	})
}

func hostCoveredBy(need string, allowed []string) bool {
	need = strings.ToLower(strings.TrimSpace(need))
	for _, pattern := range allowed {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == need {
			return true
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(need, suffix) && len(need) > len(suffix) && !strings.HasPrefix(need, "*.") {
				return true
			}
		}
	}
	return false
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
