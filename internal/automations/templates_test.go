package automations

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseUsesRef(t *testing.T) {
	ref, err := ParseUsesRef("/gitslice/std/cloudflare/deploy-worker@v1.2.0")
	if err != nil {
		t.Fatalf("ParseUsesRef tag: %v", err)
	}
	if ref.Account != "gitslice" || ref.Folder != "/gitslice/std/cloudflare" || ref.Name != "deploy-worker" || ref.ID != "std/cloudflare/deploy-worker" || ref.Pin != "v1.2.0" || ref.PinKind != "tag" {
		t.Fatalf("got %+v", ref)
	}

	commitRef, err := ParseUsesRef("/acme/deploy@sha256:abcdef012345")
	if err != nil {
		t.Fatalf("ParseUsesRef commit: %v", err)
	}
	if commitRef.Folder != "/acme" || commitRef.ID != "deploy" || commitRef.PinKind != "commit" {
		t.Fatalf("got %+v", commitRef)
	}

	liveRef, err := ParseUsesRef("/acme/platform/deploy")
	if err != nil {
		t.Fatalf("ParseUsesRef live: %v", err)
	}
	if liveRef.Pin != "" || liveRef.PinKind != "" || liveRef.Folder != "/acme/platform" {
		t.Fatalf("got %+v", liveRef)
	}

	for _, bad := range []string{"", "acme/foo", "/acme", "/acme/foo@", "/acme/../foo/bar", "/acme/foo@sha256:nothex"} {
		if _, err := ParseUsesRef(bad); err == nil {
			t.Fatalf("expected ParseUsesRef(%q) to fail", bad)
		}
	}
}

func TestParseCallTemplateUsesAndSteps(t *testing.T) {
	// 1. Valid on:[call] template with inputs
	tmplYAML := []byte(`version: 1
automations:
  deploy-worker:
    description: Deploy a Cloudflare Worker
    on: [call]
    inputs:
      dir:
        type: string
        default: "."
      env:
        type: choice
        options: [staging, prod]
        required: true
    secrets: [CLOUDFLARE_API_TOKEN]
    network: [api.cloudflare.com]
    timeout: 10m
    action:
      bash:
        runtime: node
        checkout: folder
        script: deploy.sh
`)
	file, err := Parse(tmplYAML)
	if err != nil {
		t.Fatalf("Parse template: %v", err)
	}
	if len(file.Automations["deploy-worker"].Inputs) != 2 {
		t.Fatalf("template inputs = %+v", file.Automations["deploy-worker"].Inputs)
	}

	// 2. Caller with secret map bindings + action.uses + action.steps
	callerYAML := []byte(`version: 1
automations:
  deploy:
    on: [manual]
    secrets:
      CLOUDFLARE_API_TOKEN: BILLING_CF_TOKEN
    network: [api.cloudflare.com]
    concurrency_group: billing-prod
    timeout: 5m
    action:
      uses: /gitslice/std/cloudflare/deploy-worker@v1.0.0
      with:
        dir: worker
  release:
    on: [manual]
    secrets:
      CLOUDFLARE_API_TOKEN: BILLING_CF_TOKEN
      SLACK_URL: BILLING_SLACK
    network: [api.cloudflare.com, hooks.slack.com]
    action:
      steps:
        - id: deploy
          name: Deploy worker
          uses: /gitslice/std/cloudflare/deploy-worker@v1.0.0
          secrets:
            CLOUDFLARE_API_TOKEN: BILLING_CF_TOKEN
          network: [api.cloudflare.com]
          with:
            dir: worker
            env: prod
        - id: notify
          if: always
          http:
            url: "${{ secrets.SLACK_URL }}"
`)
	callerFile, err := Parse(callerYAML)
	if err != nil {
		t.Fatalf("Parse caller: %v", err)
	}
	dep := callerFile.Automations["deploy"]
	if dep.Action.Kind != ActionUses || dep.SecretBindings["CLOUDFLARE_API_TOKEN"] != "BILLING_CF_TOKEN" || len(dep.Secrets) != 1 || dep.Secrets[0] != "BILLING_CF_TOKEN" {
		t.Fatalf("caller deploy = %+v", dep)
	}
	rel := callerFile.Automations["release"]
	if rel.Action.Kind != ActionSteps || len(rel.Action.Steps) != 2 || rel.Action.Steps[1].If != StepIfAlways {
		t.Fatalf("caller release = %+v", rel)
	}

	// 3. Reject nested uses inside on:[call]
	if _, err := Parse([]byte(`version: 1
automations:
  nested:
    on: [call]
    action:
      uses: /acme/other/op
`)); err == nil {
		t.Fatal("expected nested uses in on:[call] to fail")
	}

	// 4. Reject step escalating secret not in parent
	if _, err := Parse([]byte(`version: 1
automations:
  bad:
    on: [manual]
    secrets: [A]
    action:
      steps:
        - id: s1
          secrets: [B]
          bash:
            run: echo hi
`)); err == nil {
		t.Fatal("expected step secret escalation to fail")
	}
}

func TestBindTemplateCapabilityCeilingsAndInputs(t *testing.T) {
	tmpl := Automation{
		On:      []string{EventCall},
		Secrets: []string{"CF_TOKEN"},
		Network: []string{"api.cloudflare.com"},
		Egress:  EgressHTTPGate,
		Timeout: 10 * time.Minute,
		Inputs: map[string]InputSpec{
			"dir": {Type: InputTypeString, Default: "."},
			"env": {Type: InputTypeChoice, Options: []string{"staging", "prod"}, Required: true},
		},
		Action: Action{
			Kind: ActionBash,
			Bash: &BashAction{Runtime: BashRuntimeNode, Size: BashSizeLite, Checkout: BashCheckoutFolder, Script: "deploy.sh"},
		},
	}

	// Cross-account without network fails closed.
	loadedCross := &LoadedTemplate{
		Ref:        UsesRef{Raw: "/std/cf/deploy@v1.0.0", Account: "std", Folder: "/std/cf", Name: "deploy", ID: "cf/deploy", Pin: "v1.0.0", PinKind: "tag"},
		Automation: tmpl,
		CommitID:   "sha256:11111111",
		RootTreeID: "tree_std",
	}
	callerNoNet := Automation{
		On:             []string{EventManual},
		Secrets:        []string{"MY_CF"},
		SecretBindings: map[string]string{"CF_TOKEN": "MY_CF"},
		Action:         Action{Kind: ActionUses, Uses: &UsesAction{Ref: loadedCross.Ref.Raw, With: map[string]string{"dir": "app"}}},
	}
	if _, err := BindTemplate(callerNoNet, "acme", loadedCross, map[string]string{"env": "prod"}, nil); err == nil || !strings.Contains(err.Error(), "network host api.cloudflare.com") {
		t.Fatalf("cross-account missing network err = %v, want network host error", err)
	}

	// Cross-account with wildcard network + explicit timeout cap + secret remapping succeeds.
	callerOK := callerNoNet
	callerOK.Network = []string{"*.cloudflare.com"}
	callerOK.Timeout = 4 * time.Minute
	callerOK.TimeoutExplicit = true
	bound, err := BindTemplate(callerOK, "acme", loadedCross, map[string]string{"env": "prod"}, nil)
	if err != nil {
		t.Fatalf("BindTemplate valid: %v", err)
	}
	if bound.Automation.Timeout != 4*time.Minute {
		t.Fatalf("timeout = %v, want 4m cap", bound.Automation.Timeout)
	}
	if bound.Inputs["dir"] != "app" || bound.Inputs["env"] != "prod" {
		t.Fatalf("inputs = %+v", bound.Inputs)
	}
	if bound.Automation.SecretBindings["CF_TOKEN"] != "MY_CF" {
		t.Fatalf("secret bindings = %+v", bound.Automation.SecretBindings)
	}

	// Same-account inherits network when caller omits network:.
	loadedSame := &LoadedTemplate{
		Ref:        UsesRef{Raw: "/acme/cf/deploy", Account: "acme", Folder: "/acme/cf", Name: "deploy", ID: "cf/deploy"},
		Automation: tmpl,
		CommitID:   "sha256:22222222",
		RootTreeID: "tree_acme",
	}
	boundSame, err := BindTemplate(callerNoNet, "acme", loadedSame, map[string]string{"env": "staging"}, nil)
	if err != nil {
		t.Fatalf("same-account inherit network: %v", err)
	}
	if len(boundSame.Automation.Network) != 1 || boundSame.Automation.Network[0] != "api.cloudflare.com" {
		t.Fatalf("same-account network = %+v", boundSame.Automation.Network)
	}

	// Template requesting reveal without caller opt-in fails closed.
	loadedReveal := *loadedCross
	loadedReveal.Automation.Reveal = []string{"CF_TOKEN"}
	if _, err := BindTemplate(callerOK, "acme", &loadedReveal, map[string]string{"env": "prod"}, nil); err == nil || !strings.Contains(err.Error(), "reveals secret CF_TOKEN") {
		t.Fatalf("reveal escalation err = %v, want reveal error", err)
	}

	// Template requesting egress: direct without caller opt-in fails closed.
	loadedDirect := *loadedCross
	loadedDirect.Automation.Egress = EgressDirect
	if _, err := BindTemplate(callerOK, "acme", &loadedDirect, map[string]string{"env": "prod"}, nil); err == nil || !strings.Contains(err.Error(), "egress: direct") {
		t.Fatalf("egress escalation err = %v, want egress direct error", err)
	}

	// Cross-account unpinned ref rejected by ResolveTemplate.
	if _, err := ResolveTemplate(context.Background(), mapTree{}, nil, "acme", "sha256:1", "tree_1", UsesRef{
		Raw: "/std/cf/deploy", Account: "std", Folder: "/std/cf", Name: "deploy", ID: "cf/deploy",
	}); err == nil || !strings.Contains(err.Error(), "must be pinned") {
		t.Fatalf("cross-account unpinned err = %v", err)
	}

	// EffectiveInputsForUses auto-inherits unbound template inputs.
	eff := EffectiveInputsForUses(callerOK, tmpl)
	if _, hasDir := eff["dir"]; hasDir {
		t.Fatalf("fixed with.dir should not be prompted at runtime: %+v", eff)
	}
	if _, hasEnv := eff["env"]; !hasEnv {
		t.Fatalf("unbound template input env should inherit: %+v", eff)
	}
}
