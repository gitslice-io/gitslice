# Gitslice Web — UI Review & Overhaul Plan

Date: 2026-06-20
Method: visual review of the deployed staging app (`agenttools.dev`) via
Playwright at desktop (1440) and mobile (390), against
[`design/14_web_style_guide.md`](design/14_web_style_guide.md) ("Alexandria —
High-End Editorial"). Authenticated flows were captured using a **minted bearer
token** injected into the web client (see "Enabler" below), exercising real
`nic` account data.

## Verdict

The app is **functionally complete and information-dense**, and the changeset
review surface is genuinely polished (patchset timeline, diff toggle,
syntax-highlighted diffs, changed-files tree, real mobile adaptation). But
**visually it reads as a generic developer tool, not the "Digital Curator"
editorial experience the style guide describes.** None of the guide's
signature moves are present: no Noto Serif headlines, no `#094cb2` primary /
gradient CTAs (primary actions are flat black), 1px-bordered cards everywhere
(the guide's "No-Line Rule" forbids these — boundaries should come from surface
tiers), no archival-gold (`#6d5e00`) accents, no glassmorphism menus.

## Cross-cutting findings (apply to every flow)

1. **Typography** — Headlines use the sans stack, not Noto Serif. Body is fine
   (Inter-like). Labels are not the "archival metadata" treatment (Public Sans).
   *Guide: serif headlines, Inter body, Public Sans labels.*
2. **Color & primary actions** — Primary buttons ("New slice", "Hide diff",
   nav "Slices" pill) are flat black. *Guide: primary = `#094cb2` gradient fill;
   one primary action per view.*
3. **Borders vs. surfaces** — Cards, inputs, panels all use 1px slate borders.
   *Guide: no 1px borders; define boundaries via `surface-container-lowest →
   surface-dim` background shifts and tonal elevation, "ghost border" only when
   unavoidable.*
4. **Accent system** — No tertiary/gold for badges/highlights; status chips
   (Draft, ADDED) are generic. *Guide: archival gold for highlights and badges.*
5. **Radius & elevation** — Mixed/!sharp corners and `shadow-sm`; guide wants
   min `sm` roundness and depth through tonal layering, diffused modal shadows.
6. **Density** — Dev-centric columns (content hash, raw commit ids) are
   front-and-center where an editorial hierarchy would lead with names/intent
   and demote hashes to metadata.

## Per-flow notes

- **Auth (Clerk)** — Default Clerk theme (flat black "Continue", system fonts,
  "Development mode" banner). The Clerk `appearance` prop is unused, so sign-in
  is off-brand. *Theme Clerk to Alexandria (serif heading, primary button,
  surfaces).* Lowest effort, high first-impression impact.
- **Global shell (TopBar/AppShell/Sidebar)** — Black "Slices" pill nav, plain
  wordmark, no serif, no account switcher visible. The shell is the highest-
  leverage surface (appears on every page). *Restyle first as the foundation
  proof.*
- **Dashboard / Slices list** — Empty "Select an account" state; eyebrow +
  bold-sans heading + flat-black "New slice". Needs serif header, primary CTA,
  surface-tier list rows (not bordered cards), real empty/loading states.
- **Slice detail** — Solid IA (file tree + slice-root table + History/Checkout/
  Changesets). Bordered panels, hash column dominant. *Surface-tier panels,
  serif section titles, demote content-hash, primary-style key actions.*
- **Changeset detail** — Best surface; keep the structure. Restyle chips to the
  gold/primary system, convert bordered cards to surfaces, primary the diff
  actions, serif the title.
- **Dependencies / stacks** — Same chrome; restyle with the shared primitives.
- **Mobile** — Already adapts (tabs, collapsing tables). Carry the new tokens
  through; verify TopBar doesn't crowd at <360.

## Bugs / integration gaps found

- **Infinite render loop**: repeated React "Maximum update depth exceeded"
  warnings fire on the dashboard/slices path (setState-in-effect). Observed
  under the minted-token session; **verify under a Clerk session** and fix the
  offending effect dependency.
- **Account resolution**: the Slices/dashboard derives the "home account" from
  the Clerk user, not from `AuthService.GetAuthStatus.accounts`, so a
  minted-token (or any non-Clerk) session shows an empty "Select an account"
  even though the API returns `accounts: ["nic"]`. Resolve the account from
  `GetAuthStatus` as the source of truth.
- **TopBar auth state**: shows "Sign in" while authenticated via minted token
  (reads Clerk `isSignedIn` only). Make the shell's auth state minted-token
  aware (mirror the `RequireAuth` change).

## Enabler shipped this pass: minted-token web auth

To review authenticated flows headlessly (Clerk sign-up is gated by a Cloudflare
CAPTCHA), the web client now accepts a **minted bearer token** as an alternative
to the interactive Clerk session — mirroring the CLI's `gs auth token` /
service tokens:

- `src/auth/token.ts` — get/set/clear a token in `localStorage` (`gitslice.token`)
  and capture a one-time `?token=` param (stripped from the URL after storing).
- `useApi` prefers the minted token as the API bearer; `RequireAuth` treats its
  presence as an authenticated session; `main.tsx` captures `?token=` at startup.
- Dev-only `VITE_DEV_API_PROXY` in `vite.config.ts` forwards RPC calls through
  the dev server to a backend origin (avoids CORS when pointing local web at
  staging). Inert unless set.

(Follow-up: make the **TopBar/account resolution** minted-token aware too, per
the integration gaps above.)

## Overhaul plan (foundation first, then fan out)

1. **Foundation (one unit, blocking):** Tailwind theme = Alexandria palette
   (primary `#094cb2` + container, tertiary gold `#6d5e00`, surface tiers),
   fonts (Noto Serif / Inter / Public Sans, loaded), min `sm` radius; plus core
   primitives in `src/components/ui/` — `Button` (primary gradient / secondary /
   tertiary), `Card`/`Surface` (no-border tonal), `Input`, `Badge`, `PageHeader`
   (serif). Restyle the **global shell** with these as the proof.
2. **Fan out per flow against the primitives (disjoint files):** auth (Clerk
   theme) · slices (list/detail/settings/create) · changesets (list/detail) ·
   dependencies/stacks · source & diff viewers.
3. **Fix the two correctness bugs** (render loop, account resolution) alongside
   the shell pass.
4. **Verify each flow visually** (desktop + mobile) via the dev-server +
   minted-token screenshot harness used for this review.
