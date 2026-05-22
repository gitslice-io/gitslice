# Gemini Review: Gitslice (GS) Architecture & Design Plan

This document presents a comprehensive review of the architecture, storage, CLI, Git compatibility, and execution design plans for **Gitslice (GS)**, a cloud-native, Git-compatible version control system.

---

## 1. System Overview

Gitslice adopts the architectural mantra:
> **Native global source graph first. Git compatibility at the boundary.**

Instead of acting as a traditional Git server internally, Gitslice maintains a native, globally consistent, and scalable commit graph. Git repository projections are served at the boundaries for compatibility with traditional Git clients and ecosystems, while native tooling utilizes sparse workspaces and a changeset-centric submission model.

---

## 2. Strengths & Architectural Merits

* **Canonical Global Paths:** Rooting the entire repository under absolute, account-based paths (`/{account}/...`) removes path-aliasing complexity. Slices preserve this absolute path structure, resolving path layout ambiguity.
* **Changesets & Patchsets over Direct Commits:** Forcing writes through changesets with immutable patchsets ensures that review status, CI check validation, and policy compliance are always bound to a stable, auditable version of the proposed changes.
* **Sparse, Virtualized Workspaces:** Decoupling workspace checkout from cloning the full global tree allows developers and agents to interact with lightweight, multi-slice environments, hydrating files only when accessed.
* **Versioned Submit Queues:** Storing queue configurations as code (`.gitslice/queues/*.yaml`) allows different parts of the organization to define how their changes land without introducing a central queue bottleneck.

---

## 3. Identified Gaps & Design Omissions

While the architecture is highly cohesive, several critical areas are under-specified or missing from the initial design files. These are documented below to ensure they are addressed prior to full implementation.

### 3.1 Git LFS (Large File Storage) & Binary Asset Projection
* **The Gap:** The design documents detail "Optional Path Locks" for binary files but do not explain how large binaries are stored or served to Git clients. If Git clients clone a slice with massive binary files, transferring them as raw git objects will bloat repositories and slow down execution.
* **Proposed Solution:** Gitslice must implement Git LFS protocol compatibility. The Git Gateway should automatically project large native Gitslice blobs as Git LFS pointers to Git clients, and handle download/upload redirection directly to/from the Object Store (e.g., S3/GCS).

### 3.2 Distributed Garbage Collection (GC)
* **The Gap:** The storage architecture relies on content-addressed, immutable blobs and metadata. With multiple patchsets, draft snapshots, and overlapping slices, the storage layers will inevitably collect orphaned data. No garbage collection strategy is defined.
* **Proposed Solution:** Introduce a background Distributed Garbage Collection Service. Using a multi-phase mark-and-sweep algorithm, it should scan active branch refs, open changesets, and current slice definitions to mark active blobs, then asynchronously delete unreferenced objects in GCS/S3 and metadata storage.

### 3.3 Starvation and CAS Conflict in Shared Submit Queues
* **The Gap:** Multiple independent queues can target the same ref (e.g., `refs/global/main`). While queue leases serialize entries within a single queue, concurrent submissions from *different* queues will race to CAS the target ref. Under high load, this will cause repeated rebases, CI reruns, and queue starvation.
* **Proposed Solution:** Establish a final-stage serialization lock or transaction sequencer per target ref in the Submit Queue Service. Even if queues process items concurrently, they must hand off final validation and ref updates to a linearizing queue worker.

### 3.4 Folder Policy Tampering & Security
* **The Gap:** Folder policies (`.gitslice/policy.yaml`) are source-controlled files. Although the validation uses the *previous* accepted policy file for the same changeset, an actor could submit Change A (which weakens the policy file) and immediately submit Change B (which introduces unvalidated code under the weakened policy).
* **Proposed Solution:** Restrict modification of `.gitslice/policy.yaml` files. Changes to these paths must bypass standard writer roles and require explicit approval from slice owners, admins, or owners of the parent folder's policy.

### 3.5 Git Gateway Packfile Caching
* **The Gap:** The Git Gateway projects synthetic Git commits dynamically. Generating synthetic packfiles on the fly during a `git clone` or `git fetch` for large slices is highly CPU and memory intensive, creating a Denial of Service (DoS) vulnerability.
* **Proposed Solution:** The Git Gateway should cache synthetic packfiles at stable projection checkpoints (e.g., the latest global commit on `refs/global/main`). Fetches should only compute incremental packfiles relative to these pre-cached base checkpoints.

### 3.6 Cross-Slice Dependency and Linked Changesets
* **The Gap:** To maintain clean boundaries, cross-slice changesets are prohibited. However, cross-slice dependency is common (e.g., an API change in a library slice requires a consumer slice to update). The coordination of "linked changesets" is not detailed.
* **Proposed Solution:** The Changeset Service should support:
  * **Atomic Linked Submissions:** Allowing linked changesets to submit together, running combined CI checks, and committing their changes in a single atomic database transaction.
  * **Explicit Order Dependencies:** Specifying that Changeset A must land before Changeset B can be queued.

### 3.7 Local File Watcher Integration for the CLI
* **The Gap:** Snapshotting workspace modifications (`gs status` and `gs cs update`) by scanning filesystem directories becomes slow as workspace size increases.
* **Proposed Solution:** The CLI must integrate with operating system file-watching APIs (e.g., `FSEvents` on macOS, `inotify` on Linux) to dynamically maintain a local index of changed files, ensuring near-instantaneous workspace status commands.
