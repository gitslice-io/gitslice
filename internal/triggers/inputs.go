package triggers

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Input types for manual automations.
const (
	InputTypeString  = "string"
	InputTypeChoice  = "choice"
	InputTypeBoolean = "boolean"
	InputTypeNumber  = "number"
)

// Manual roles.
const (
	ManualRoleAdmin  = "admin"
	ManualRoleWriter = "writer"
)

// Bash egress modes.
const (
	EgressOffline  = "offline"
	EgressHTTPGate = "http-gate"
	EgressDirect   = "direct"
)

// Bash runtimes (curated Cloudflare Container profiles).
const (
	BashRuntimeBase   = "base"
	BashRuntimeNode   = "node"
	BashRuntimePython = "python"
	BashRuntimeGo     = "go"
	BashRuntimeCloud  = "cloud"
)

// Bash container sizes.
const (
	BashSizeLite      = "lite"
	BashSizeBasic     = "basic"
	BashSizeStandard1 = "standard-1"
	BashSizeStandard2 = "standard-2"
)

// Bash checkout modes.
const (
	BashCheckoutNone    = "none"
	BashCheckoutFolder  = "folder"
	BashCheckoutAccount = "account"
)

// Bash limits.
const (
	DefaultBashTimeout = 5 * time.Minute
	MaxBashTimeout     = 15 * time.Minute
	MaxInputs          = 32
	MaxInputValueBytes = 4 << 10
	MaxConfirmBytes    = 240
	MaxDescriptionLen  = 500
	MaxBashRunBytes    = 64 << 10
)

// InputSpec describes one typed input of a manual automation.
type InputSpec struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	HasDefault  bool     `json:"has_default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
}

// ManualPolicy governs who may invoke a manual automation and whether UI/CLI
// should ask for explicit confirmation.
type ManualPolicy struct {
	Roles        []string `json:"roles,omitempty"`
	RequireHuman bool     `json:"require_human,omitempty"`
	Confirm      string   `json:"confirm,omitempty"`
}

// AllowsWriter reports whether non-admin writers may invoke the automation.
func (p *ManualPolicy) AllowsWriter() bool {
	if p == nil || len(p.Roles) == 0 {
		return true
	}
	return contains(p.Roles, ManualRoleWriter)
}

// BashAction runs a bash command or script inside an ephemeral Cloudflare
// Container.
type BashAction struct {
	Run        string            `json:"run,omitempty"`
	Script     string            `json:"script,omitempty"`
	Runtime    string            `json:"runtime"`
	Size       string            `json:"size"`
	Checkout   string            `json:"checkout"`
	Include    []string          `json:"include,omitempty"`
	WorkingDir string            `json:"working_dir"`
	Env        map[string]string `json:"env,omitempty"`
}

var (
	inputNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*$`)
	envNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// InputEnvName maps an automation input name to its bash environment variable
// name (for example, "dry-run" -> "INPUT_DRY_RUN").
func InputEnvName(name string) string {
	upper := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	return "INPUT_" + upper
}

type rawInputSpec struct {
	Type        string   `yaml:"type"`
	Description string   `yaml:"description"`
	Required    bool     `yaml:"required"`
	Default     any      `yaml:"default"`
	Options     []string `yaml:"options"`
	Pattern     string   `yaml:"pattern"`
}

type rawManualPolicy struct {
	Roles        []string `yaml:"roles"`
	RequireHuman bool     `yaml:"require_human"`
	Confirm      string   `yaml:"confirm"`
}

type rawBashAction struct {
	Run        string            `yaml:"run"`
	Script     string            `yaml:"script"`
	Runtime    string            `yaml:"runtime"`
	Size       string            `yaml:"size"`
	Checkout   string            `yaml:"checkout"`
	Include    []string          `yaml:"include"`
	WorkingDir string            `yaml:"working_dir"`
	Env        map[string]string `yaml:"env"`
}

func parseInputs(raw map[string]rawInputSpec) (map[string]InputSpec, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > MaxInputs {
		return nil, fmt.Errorf("inputs: at most %d inputs are allowed", MaxInputs)
	}
	out := make(map[string]InputSpec, len(raw))
	envSeen := make(map[string]string, len(raw))
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !inputNamePattern.MatchString(name) {
			return nil, fmt.Errorf("input %q: names must match ^[a-zA-Z_][a-zA-Z0-9_-]*$", name)
		}
		envName := InputEnvName(name)
		if prev, exists := envSeen[envName]; exists {
			return nil, fmt.Errorf("input %q collides with input %q (both map to %s)", name, prev, envName)
		}
		envSeen[envName] = name

		spec, err := parseOneInput(name, raw[name])
		if err != nil {
			return nil, err
		}
		out[name] = spec
	}
	return out, nil
}

func parseOneInput(name string, raw rawInputSpec) (InputSpec, error) {
	typ := strings.TrimSpace(raw.Type)
	if typ == "" {
		typ = InputTypeString
	}
	switch typ {
	case InputTypeString, InputTypeChoice, InputTypeBoolean, InputTypeNumber:
	default:
		return InputSpec{}, fmt.Errorf("input %q: unknown type %q (use string, choice, boolean or number)", name, typ)
	}
	desc := strings.TrimSpace(raw.Description)
	if len(desc) > MaxDescriptionLen {
		return InputSpec{}, fmt.Errorf("input %q: description is longer than %d bytes", name, MaxDescriptionLen)
	}
	var options []string
	if typ == InputTypeChoice {
		if len(raw.Options) == 0 {
			return InputSpec{}, fmt.Errorf("input %q: choice input requires options", name)
		}
		for _, opt := range raw.Options {
			if strings.ContainsRune(opt, '\x00') {
				return InputSpec{}, fmt.Errorf("input %q: option contains NUL", name)
			}
			options = appendUnique(options, opt)
		}
	} else if len(raw.Options) > 0 {
		return InputSpec{}, fmt.Errorf("input %q: options only apply to choice inputs", name)
	}
	pattern := strings.TrimSpace(raw.Pattern)
	if pattern != "" {
		if typ != InputTypeString {
			return InputSpec{}, fmt.Errorf("input %q: pattern only applies to string inputs", name)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return InputSpec{}, fmt.Errorf("input %q: invalid pattern %q: %v", name, pattern, err)
		}
	}
	spec := InputSpec{
		Type:        typ,
		Description: desc,
		Required:    raw.Required,
		Options:     options,
		Pattern:     pattern,
	}
	if raw.Default != nil {
		val, err := stringifyDefault(raw.Default)
		if err != nil {
			return InputSpec{}, fmt.Errorf("input %q: default: %v", name, err)
		}
		normalized, err := validateInputValue(name, spec, val)
		if err != nil {
			return InputSpec{}, fmt.Errorf("input %q default: %v", name, err)
		}
		spec.Default = normalized
		spec.HasDefault = true
	}
	return spec, nil
}

func stringifyDefault(v any) (string, error) {
	switch val := v.(type) {
	case string:
		return val, nil
	case bool:
		return strconv.FormatBool(val), nil
	case int:
		return strconv.Itoa(val), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	case uint64:
		return strconv.FormatUint(val, 10), nil
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("must be a scalar string, boolean or number")
	}
}

func parseManualPolicy(raw *rawManualPolicy, on []string) (*ManualPolicy, error) {
	if raw == nil {
		return nil, nil
	}
	if !contains(on, EventManual) {
		return nil, fmt.Errorf("manual policy requires manual in on")
	}
	var roles []string
	for _, role := range raw.Roles {
		role = strings.ToLower(strings.TrimSpace(role))
		switch role {
		case ManualRoleAdmin, ManualRoleWriter:
			roles = appendUnique(roles, role)
		default:
			return nil, fmt.Errorf("manual.roles %q (use admin or writer)", role)
		}
	}
	confirm := strings.TrimSpace(raw.Confirm)
	if len(confirm) > MaxConfirmBytes {
		return nil, fmt.Errorf("manual.confirm is longer than %d bytes", MaxConfirmBytes)
	}
	return &ManualPolicy{
		Roles:        roles,
		RequireHuman: raw.RequireHuman,
		Confirm:      confirm,
	}, nil
}

// ValidateInputs validates and normalizes user-supplied inputs for an
// automation. When specs is empty, arbitrary key/value inputs (bounded in count
// and size) are still accepted for lightweight scripts; when specs is non-empty,
// unknown inputs are rejected and defaults/types/patterns are enforced.
func ValidateInputs(specs map[string]InputSpec, provided map[string]string) (map[string]string, error) {
	if len(specs) == 0 {
		if len(provided) == 0 {
			return nil, nil
		}
		if len(provided) > MaxInputs {
			return nil, fmt.Errorf("at most %d inputs are allowed", MaxInputs)
		}
		out := make(map[string]string, len(provided))
		for k, v := range provided {
			if !inputNamePattern.MatchString(k) {
				return nil, fmt.Errorf("invalid input name %q", k)
			}
			if len(v) > MaxInputValueBytes || strings.ContainsRune(v, '\x00') {
				return nil, fmt.Errorf("input %q exceeds size limit or contains NUL", k)
			}
			out[k] = v
		}
		return out, nil
	}
	for k := range provided {
		if _, ok := specs[k]; !ok {
			return nil, fmt.Errorf("unknown input %q", k)
		}
	}
	out := make(map[string]string, len(specs))
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := specs[name]
		raw, ok := provided[name]
		if !ok || raw == "" {
			if spec.HasDefault {
				out[name] = spec.Default
				continue
			}
			if spec.Required {
				return nil, fmt.Errorf("input %q is required", name)
			}
			continue
		}
		val, err := validateInputValue(name, spec, raw)
		if err != nil {
			return nil, err
		}
		out[name] = val
	}
	return out, nil
}

func validateInputValue(name string, spec InputSpec, value string) (string, error) {
	if len(value) > MaxInputValueBytes {
		return "", fmt.Errorf("input %q is larger than %d bytes", name, MaxInputValueBytes)
	}
	if strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("input %q contains NUL", name)
	}
	switch spec.Type {
	case InputTypeString:
		if spec.Pattern != "" {
			re, err := regexp.Compile(spec.Pattern)
			if err != nil {
				return "", fmt.Errorf("input %q has invalid pattern", name)
			}
			if !re.MatchString(value) {
				return "", fmt.Errorf("input %q value %q does not match %s", name, value, spec.Pattern)
			}
		}
		return value, nil
	case InputTypeChoice:
		if !contains(spec.Options, value) {
			return "", fmt.Errorf("input %q value %q must be one of %s", name, value, strings.Join(spec.Options, ", "))
		}
		return value, nil
	case InputTypeBoolean:
		b, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(value)))
		if err != nil {
			return "", fmt.Errorf("input %q value %q must be true or false", name, value)
		}
		return strconv.FormatBool(b), nil
	case InputTypeNumber:
		trimmed := strings.TrimSpace(value)
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return "", fmt.Errorf("input %q value %q must be a number", name, value)
		}
		return trimmed, nil
	default:
		return value, nil
	}
}

func parseBashAction(raw rawBashAction, t Trigger) (*BashAction, string, error) {
	run := raw.Run
	script := strings.TrimSpace(raw.Script)
	if strings.TrimSpace(run) == "" && script == "" {
		return nil, "", fmt.Errorf("action.bash requires run or script")
	}
	if strings.TrimSpace(run) != "" && script != "" {
		return nil, "", fmt.Errorf("action.bash accepts either run or script, not both")
	}
	if len(run) > MaxBashRunBytes || strings.ContainsRune(run, '\x00') {
		return nil, "", fmt.Errorf("action.bash.run exceeds %d KiB or contains NUL", MaxBashRunBytes>>10)
	}
	if script != "" {
		cleaned, err := cleanBashScriptPath(script)
		if err != nil {
			return nil, "", err
		}
		script = cleaned
	}

	runtime := strings.ToLower(strings.TrimSpace(raw.Runtime))
	if runtime == "" {
		runtime = BashRuntimeBase
	}
	switch runtime {
	case BashRuntimeBase, BashRuntimeNode, BashRuntimePython, BashRuntimeGo, BashRuntimeCloud:
	default:
		return nil, "", fmt.Errorf("action.bash.runtime %q (use base, node, python, go or cloud)", runtime)
	}

	size := strings.ToLower(strings.TrimSpace(raw.Size))
	if size == "" {
		size = BashSizeBasic
	}
	switch size {
	case BashSizeLite, BashSizeBasic, BashSizeStandard1, BashSizeStandard2:
	default:
		return nil, "", fmt.Errorf("action.bash.size %q (use lite, basic, standard-1 or standard-2)", size)
	}

	checkout := strings.ToLower(strings.TrimSpace(raw.Checkout))
	if checkout == "" {
		if onlyChangesetEvents(t.On) {
			checkout = BashCheckoutNone
		} else {
			checkout = BashCheckoutFolder
		}
	}
	switch checkout {
	case BashCheckoutNone, BashCheckoutFolder, BashCheckoutAccount:
	default:
		return nil, "", fmt.Errorf("action.bash.checkout %q (use none, folder or account)", checkout)
	}
	if checkout == BashCheckoutNone && len(raw.Include) > 0 {
		return nil, "", fmt.Errorf("action.bash.include cannot be used with checkout: none")
	}

	workingDir := strings.TrimSpace(raw.WorkingDir)
	if workingDir == "" {
		workingDir = "."
	}
	if strings.HasPrefix(workingDir, "/") || strings.ContainsRune(workingDir, '\x00') {
		return nil, "", fmt.Errorf("action.bash.working_dir %q must be relative to the folder", workingDir)
	}
	workingDir = path.Clean(workingDir)
	if workingDir == ".." || strings.HasPrefix(workingDir, "../") {
		return nil, "", fmt.Errorf("action.bash.working_dir %q must not escape the folder", raw.WorkingDir)
	}

	var include []string
	for _, inc := range raw.Include {
		inc = strings.TrimSpace(inc)
		if inc == "" || strings.ContainsAny(inc, "\r\n\x00") {
			return nil, "", fmt.Errorf("action.bash.include contains invalid path %q", inc)
		}
		for _, seg := range strings.Split(inc, "/") {
			if seg == ".." {
				return nil, "", fmt.Errorf("action.bash.include %q must not contain ..", inc)
			}
		}
		include = appendUnique(include, path.Clean(inc))
	}

	var env map[string]string
	if len(raw.Env) > 0 {
		env = make(map[string]string, len(raw.Env))
		for k, v := range raw.Env {
			if !envNamePattern.MatchString(k) {
				return nil, "", fmt.Errorf("action.bash.env key %q is not a valid environment variable name", k)
			}
			if strings.HasPrefix(k, "GITSLICE_") || strings.HasPrefix(k, "INPUT_") {
				return nil, "", fmt.Errorf("action.bash.env key %q is reserved", k)
			}
			if contains(t.Secrets, k) {
				return nil, "", fmt.Errorf("action.bash.env key %q collides with secret %s", k, k)
			}
			if strings.ContainsRune(v, '\x00') {
				return nil, "", fmt.Errorf("action.bash.env %q contains NUL", k)
			}
			env[k] = v
		}
	}

	egress := strings.ToLower(strings.TrimSpace(t.Egress))
	switch egress {
	case "":
		if len(t.Network) > 0 {
			egress = EgressHTTPGate
		} else {
			egress = EgressOffline
		}
	case EgressOffline:
		if len(t.Network) > 0 {
			return nil, "", fmt.Errorf("egress: offline cannot be combined with network hosts")
		}
	case EgressHTTPGate:
		if len(t.Network) == 0 {
			return nil, "", fmt.Errorf("egress: http-gate requires at least one host in network")
		}
	case EgressDirect:
		if len(t.Network) > 0 {
			return nil, "", fmt.Errorf("egress: direct cannot be combined with network host allowlist (direct egress bypasses the HTTP gate)")
		}
		for _, sec := range t.Secrets {
			if !contains(t.Reveal, sec) {
				return nil, "", fmt.Errorf("egress: direct requires secret %s to be listed in reveal (sealed placeholders only unwrap through http-gate)", sec)
			}
		}
	default:
		return nil, "", fmt.Errorf("egress %q (use offline, http-gate or direct)", egress)
	}

	return &BashAction{
		Run:        run,
		Script:     script,
		Runtime:    runtime,
		Size:       size,
		Checkout:   checkout,
		Include:    include,
		WorkingDir: workingDir,
		Env:        env,
	}, egress, nil
}

func cleanBashScriptPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("action.bash.script %q must be relative to the .gitslice folder", raw)
	}
	cleaned := path.Clean(strings.TrimPrefix(raw, "./"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("action.bash.script %q must stay inside the .gitslice folder", raw)
	}
	return cleaned, nil
}

func onlyChangesetEvents(on []string) bool {
	if len(on) == 0 {
		return false
	}
	for _, ev := range on {
		switch ev {
		case EventChangesetCreated, EventChangesetUpdated, EventChangesetApproved, EventChangesetAbandoned:
		default:
			return false
		}
	}
	return true
}
