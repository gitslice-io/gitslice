# Archived Design Documents

Documents in this directory are historical records of past designs, shelved
investigations, removed features, or specs that have been consolidated into the
active design documents under [`design/`](../). They are kept for archaeological
reference and should not be treated as the current specification.

Note: `10_execution_log.md` was moved out of `design/` to
[`docs/execution_log.md`](../../docs/execution_log.md).

| Archived file | Status / Reason | Superseded by |
| --- | --- | --- |
| `08_mvp_implementation.md` | Historical MVP implementation blueprint | [`01_gitslice_architecture_design.md`](../01_gitslice_architecture_design.md), [`02_storage.md`](../02_storage.md), [`03_core_api.md`](../03_core_api.md) |
| `09_execution_plan.md` | Historical MVP phase-by-phase rollout plan | [`docs/execution_log.md`](../../docs/execution_log.md) |
| `14_web_style_guide.md` | Historical editorial web style guide | [`11_web_interface_design.md`](../11_web_interface_design.md) |
| `18_agent_workspace_preupload.md` | Historical proposal (partial capture diagnostics shipped; rest superseded) | [`04_cli_design.md`](../04_cli_design.md), [`16_bring_your_own_agent.md`](../16_bring_your_own_agent.md) |
| `19_object_storage_wal.md` | Shelved (2026-08-20; throughput issue was a measurement artifact) | [`02_storage.md`](../02_storage.md), [`28_benchmarks.md`](../28_benchmarks.md) |
| `20_agent_signup_and_claim.md` | Consolidated into unified account & auth spec | [`12_account_auth.md`](../12_account_auth.md) |
| `23_large_git_projection.md` | Shelved / partial (initial steps landed; remainder unbuilt) | [`05_git_compatibility.md`](../05_git_compatibility.md) |
| `24_webhooks.md` | Removed (2026-11-17; replaced by automations `http` actions; `internal/events` & `internal/outbound` kept) | [`34_automations.md`](../34_automations.md) |
| `26_slice_members.md` | Consolidated into unified account & auth spec | [`12_account_auth.md`](../12_account_auth.md) |
| `31_ci_daemon.md` | Consolidated into unified CI spec | [`17_continuous_integration.md`](../17_continuous_integration.md) |
| `32_ci_dispatch_on_cloudflare.md` | Consolidated into unified CI spec | [`17_continuous_integration.md`](../17_continuous_integration.md) |
| `34_triggers.md` | Consolidated & renamed (`Trigger` → `Automation`) | [`34_automations.md`](../34_automations.md) |
| `37_automations_and_containers.md` | Consolidated into unified Automations spec | [`34_automations.md`](../34_automations.md) |
| `38_reusable_automations_and_pipelines.md` | Consolidated into unified Automations spec | [`34_automations.md`](../34_automations.md) |
