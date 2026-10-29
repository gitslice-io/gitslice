// Package triggers reads .gitslice/triggers.yaml files and decides which
// triggers an event sets off (design/34_triggers.md).
package triggers

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"gitslice.io/gitslice/internal/storage"
)

// Events a trigger can listen for: the webhook events (design/24) and
// manual runs.
const (
	EventPush               = storage.WebhookEventPush
	EventTagCreated         = storage.WebhookEventTagCreated
	EventChangesetCreated   = storage.WebhookEventChangesetCreated
	EventChangesetUpdated   = storage.WebhookEventChangesetUpdated
	EventChangesetApproved  = storage.WebhookEventChangesetApproved
	EventChangesetSubmitted = storage.WebhookEventChangesetSubmitted
	EventChangesetAbandoned = storage.WebhookEventChangesetAbandoned
	EventCheckRunCompleted  = storage.WebhookEventCheckRunCompleted
	EventManual             = "manual"
)

var eventNames = map[string]bool{
	EventPush: true, EventTagCreated: true, EventChangesetCreated: true, EventChangesetUpdated: true,
	EventChangesetApproved: true, EventChangesetSubmitted: true, EventChangesetAbandoned: true,
	EventCheckRunCompleted: true, EventManual: true,
}

// Kinds of change to a file.
const (
	ChangeAdded    = "added"
	ChangeModified = "modified"
	ChangeDeleted  = "deleted"
	ChangeMoved    = "moved"
)

// Concurrency modes.
const (
	ConcurrencyCancelSuperseded = "cancel-superseded"
	ConcurrencySerial           = "serial"
	ConcurrencyParallel         = "parallel"
)

// Actions.
const (
	ActionHTTP   = "http"
	ActionWorker = "worker"
	// ActionCheck runs checks.yaml checks of the trigger's folder on the
	// slice's CI daemon.
	ActionCheck = "check"
)

// RunTokenSecret is the name a trigger lists in reveal to hand its run token
// to an outside system.
const RunTokenSecret = "RUN_TOKEN"

// Limits.
const (
	DefaultTimeout = time.Hour
	MaxTimeout     = 7 * 24 * time.Hour
	MaxRetry       = 5
)

// FileName is where triggers live in a folder.
const FileName = ".gitslice/triggers.yaml"

// File is one parsed triggers.yaml.
type File struct {
	Version  int
	Triggers map[string]Trigger // keyed by local name
}

// Trigger is one entry of a triggers file.
type Trigger struct {
	On          []string `json:"on"`
	Paths       []string `json:"paths,omitempty"`
	Changes     []string `json:"changes,omitempty"`
	Refs        []string `json:"refs,omitempty"`
	Secrets     []string `json:"secrets,omitempty"`
	Reveal      []string `json:"reveal,omitempty"`
	Network     []string `json:"network,omitempty"`
	Checks      []string `json:"checks,omitempty"`
	Concurrency string   `json:"concurrency"`
	// Timeout bounds how long a run may wait for callbacks.
	Timeout time.Duration `json:"timeout"`
	Retry   int           `json:"retry,omitempty"`
	Action  Action        `json:"action"`
}

// Action is what a trigger does: an HTTP call or a script.
type Action struct {
	Kind string      `json:"kind"`
	HTTP *HTTPAction `json:"http,omitempty"`
	// Worker is the script's path relative to the .gitslice folder.
	Worker string `json:"worker,omitempty"`
	// Check names checks of the folder's checks.yaml.
	Check []string `json:"check,omitempty"`
}

// HTTPAction posts the event envelope to a URL. URL and header values may
// hold ${{ secrets.NAME }} for secrets the trigger lists.
type HTTPAction struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	// Sign names a secret that keys X-Gitslice-Signature-256.
	Sign string `json:"sign,omitempty"`
}

// ParseError is a triggers file that cannot be used.
type ParseError struct{ Message string }

func (e *ParseError) Error() string { return e.Message }

type rawFile struct {
	Version  *int                  `yaml:"version"`
	Triggers map[string]rawTrigger `yaml:"triggers"`
}

type rawTrigger struct {
	On          []string   `yaml:"on"`
	Paths       []string   `yaml:"paths"`
	Changes     []string   `yaml:"changes"`
	Refs        []string   `yaml:"refs"`
	Secrets     []string   `yaml:"secrets"`
	Reveal      []string   `yaml:"reveal"`
	Network     []string   `yaml:"network"`
	Checks      []string   `yaml:"checks"`
	Concurrency string     `yaml:"concurrency"`
	Timeout     string     `yaml:"timeout"`
	Retry       *int       `yaml:"retry"`
	Action      *rawAction `yaml:"action"`
}

type rawAction struct {
	HTTP   *rawHTTP `yaml:"http"`
	Worker string   `yaml:"worker"`
	Check  []string `yaml:"check"`
}

type rawHTTP struct {
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
	Sign    string            `yaml:"sign"`
}

var (
	localNamePattern  = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	secretNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	hostPattern       = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,62}$`)
	// SecretRef matches ${{ secrets.NAME }} in an HTTP action.
	SecretRef = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
)

// Parse parses and validates one triggers.yaml.
func Parse(data []byte) (*File, error) {
	var raw rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, &ParseError{Message: fmt.Sprintf("parse triggers yaml: %v", err)}
	}
	if raw.Version == nil {
		return nil, &ParseError{Message: "version is required"}
	}
	if *raw.Version != 1 {
		return nil, &ParseError{Message: fmt.Sprintf("unsupported version %d", *raw.Version)}
	}
	if raw.Triggers == nil {
		return nil, &ParseError{Message: "triggers is required"}
	}
	out := &File{Version: 1, Triggers: make(map[string]Trigger, len(raw.Triggers))}
	names := make([]string, 0, len(raw.Triggers))
	for name := range raw.Triggers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !localNamePattern.MatchString(name) {
			return nil, &ParseError{Message: fmt.Sprintf("trigger %q: names use letters, digits, _ and -", name)}
		}
		trigger, err := parseTrigger(raw.Triggers[name])
		if err != nil {
			return nil, &ParseError{Message: fmt.Sprintf("trigger %s: %v", name, err)}
		}
		out.Triggers[name] = trigger
	}
	return out, nil
}

func parseTrigger(raw rawTrigger) (Trigger, error) {
	var t Trigger
	if len(raw.On) == 0 {
		return t, fmt.Errorf("on is required")
	}
	for _, event := range raw.On {
		event = strings.TrimSpace(event)
		if event == "schedule" {
			return t, fmt.Errorf("schedule is not supported yet")
		}
		if !eventNames[event] {
			return t, fmt.Errorf("unknown event %q", event)
		}
		t.On = appendUnique(t.On, event)
	}
	for _, pattern := range raw.Paths {
		pattern = strings.TrimSpace(pattern)
		glob := strings.TrimPrefix(pattern, "!")
		if glob == "" || !doublestar.ValidatePattern(strings.TrimPrefix(glob, "/")) {
			return t, fmt.Errorf("invalid paths glob %q", pattern)
		}
		if strings.Contains(glob, "..") {
			return t, fmt.Errorf("paths glob %q must not contain ..", pattern)
		}
		t.Paths = append(t.Paths, pattern)
	}
	for _, change := range raw.Changes {
		switch change {
		case ChangeAdded, ChangeModified, ChangeDeleted, ChangeMoved:
			t.Changes = appendUnique(t.Changes, change)
		default:
			return t, fmt.Errorf("unknown change %q (use added, modified, deleted or moved)", change)
		}
	}
	for _, ref := range raw.Refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return t, fmt.Errorf("empty ref")
		}
		t.Refs = appendUnique(t.Refs, ref)
	}
	for _, name := range raw.Secrets {
		if !secretNamePattern.MatchString(name) {
			return t, fmt.Errorf("secret name %q must match ^[A-Z_][A-Z0-9_]*$", name)
		}
		t.Secrets = appendUnique(t.Secrets, name)
	}
	for _, name := range raw.Reveal {
		if name != RunTokenSecret && !contains(t.Secrets, name) {
			return t, fmt.Errorf("reveal %s: list it in secrets first", name)
		}
		t.Reveal = appendUnique(t.Reveal, name)
	}
	for _, host := range raw.Network {
		host = strings.ToLower(strings.TrimSpace(host))
		if !hostPattern.MatchString(host) {
			return t, fmt.Errorf("network host %q must be a host name such as api.example.com or *.example.com", host)
		}
		t.Network = appendUnique(t.Network, host)
	}
	for _, name := range raw.Checks {
		if !localNamePattern.MatchString(name) {
			return t, fmt.Errorf("check %q: names use letters, digits, _ and -", name)
		}
		t.Checks = appendUnique(t.Checks, name)
	}
	switch raw.Concurrency {
	case "":
		t.Concurrency = ConcurrencyParallel
		for _, event := range t.On {
			if event == EventChangesetCreated || event == EventChangesetUpdated {
				t.Concurrency = ConcurrencyCancelSuperseded
			}
		}
	case ConcurrencyCancelSuperseded, ConcurrencySerial, ConcurrencyParallel:
		t.Concurrency = raw.Concurrency
	default:
		return t, fmt.Errorf("concurrency %q (use cancel-superseded, serial or parallel)", raw.Concurrency)
	}
	t.Timeout = DefaultTimeout
	if raw.Timeout != "" {
		timeout, err := parseDuration(raw.Timeout)
		if err != nil {
			return t, fmt.Errorf("timeout: %v", err)
		}
		if timeout <= 0 || timeout > MaxTimeout {
			return t, fmt.Errorf("timeout must be between 1s and 7d")
		}
		t.Timeout = timeout
	}
	if raw.Action == nil {
		return t, fmt.Errorf("action is required")
	}
	kinds := 0
	for _, set := range []bool{raw.Action.HTTP != nil, raw.Action.Worker != "", raw.Action.Check != nil} {
		if set {
			kinds++
		}
	}
	switch {
	case kinds > 1:
		return t, fmt.Errorf("action takes one of http, worker or check")
	case raw.Action.Check != nil:
		if len(raw.Action.Check) == 0 {
			return t, fmt.Errorf("action.check names no checks")
		}
		for _, name := range raw.Action.Check {
			if !localNamePattern.MatchString(name) {
				return t, fmt.Errorf("action.check %q: names use letters, digits, _ and -", name)
			}
			t.Action.Check = appendUnique(t.Action.Check, name)
			// The trigger reports these checks.
			t.Checks = appendUnique(t.Checks, name)
		}
		t.Action.Kind = ActionCheck
		for _, event := range t.On {
			if event != EventChangesetCreated && event != EventChangesetUpdated && event != EventManual {
				return t, fmt.Errorf("a check action runs on a patchset: use changeset.created, changeset.updated or manual, not %s", event)
			}
		}
		if len(t.Network) > 0 || len(t.Reveal) > 0 || len(t.Secrets) > 0 || raw.Retry != nil {
			return t, fmt.Errorf("secrets, network, reveal and retry do not apply to a check action (checks get the secrets of their folder)")
		}
	case raw.Action.HTTP != nil:
		action, err := parseHTTP(*raw.Action.HTTP, t.Secrets)
		if err != nil {
			return t, err
		}
		t.Action = Action{Kind: ActionHTTP, HTTP: action}
		if len(t.Network) > 0 || len(t.Reveal) > 0 || raw.Retry != nil {
			return t, fmt.Errorf("network, reveal and retry apply to worker actions only")
		}
	case raw.Action.Worker != "":
		script, err := cleanScriptPath(raw.Action.Worker)
		if err != nil {
			return t, err
		}
		t.Action = Action{Kind: ActionWorker, Worker: script}
	default:
		return t, fmt.Errorf("action needs http, worker or check")
	}
	if raw.Retry != nil {
		if *raw.Retry < 0 || *raw.Retry > MaxRetry {
			return t, fmt.Errorf("retry must be between 0 and %d", MaxRetry)
		}
		t.Retry = *raw.Retry
	}
	return t, nil
}

func parseHTTP(raw rawHTTP, secrets []string) (*HTTPAction, error) {
	url := strings.TrimSpace(raw.URL)
	if url == "" {
		return nil, fmt.Errorf("http.url is required")
	}
	// A secret may stand for the whole URL. Sending refuses anything but
	// https to a public address (webhooks.ValidateURL), except on servers
	// that allow private targets for tests.
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "${{") {
		return nil, fmt.Errorf("http.url must start with https://")
	}
	check := func(field, value string) error {
		for _, m := range SecretRef.FindAllStringSubmatch(value, -1) {
			if !contains(secrets, m[1]) {
				return fmt.Errorf("%s uses secret %s: list it in secrets", field, m[1])
			}
		}
		return nil
	}
	if err := check("http.url", url); err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for name, value := range raw.Headers {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " :\r\n") {
			return nil, fmt.Errorf("invalid header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("header %s has a line break", name)
		}
		if err := check("header "+name, value); err != nil {
			return nil, err
		}
		headers[name] = value
	}
	sign := strings.TrimSpace(raw.Sign)
	if sign != "" && !contains(secrets, sign) {
		return nil, fmt.Errorf("http.sign uses secret %s: list it in secrets", sign)
	}
	return &HTTPAction{URL: url, Headers: headers, Sign: sign}, nil
}

// cleanScriptPath checks a worker path: a .js or .mjs module inside the
// .gitslice folder that holds the triggers file.
func cleanScriptPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("worker %q must be relative to the .gitslice folder", raw)
	}
	cleaned := path.Clean(strings.TrimPrefix(raw, "./"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("worker %q must stay inside the .gitslice folder", raw)
	}
	if ext := path.Ext(cleaned); ext != ".js" && ext != ".mjs" {
		return "", fmt.Errorf("worker %q must be a .js or .mjs module", raw)
	}
	return cleaned, nil
}

// parseDuration reads a Go duration, or a whole number of days ("7d").
func parseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if days, ok := strings.CutSuffix(value, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", value)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return d, nil
}

func appendUnique(values []string, value string) []string {
	if contains(values, value) {
		return values
	}
	return append(values, value)
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
