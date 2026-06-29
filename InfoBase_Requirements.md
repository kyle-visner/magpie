# InfoBase: Opinionated Unified Information Fabric
## Product Requirements Document (PRD) for SMBs

**Version:** 0.7  
**Date:** June 28, 2026  
**Audience:** Coding Agent Team  
**Status:** Draft for Implementation Planning

**Changes in v0.7:** 
- **Major architectural decision**: Adopted **Merkle-DAG** as the foundational storage pattern for the custom Go engine (replacing the previous "append-only or Merkle-DAG preferred" language). This directly implements Tenet 4.4 (Blast Radius + Recoverability) with structural sharing, content-addressing, and natural versioning.
- Added detailed long-term bloat management strategy: Named period-close roots (fiscal year / month-end), lightweight snapshots, configurable retention, and background GC — all modeled after Git-style workflows the team is already familiar with.
- Updated Tenet 4.4 implications and Section 5.1 (Data Model & Storage) to reflect Merkle-DAG primitives, period-close semantics, and snapshot/GC behavior.
- Resolved the key Phase 1 open question on versioning strategy in Section 8.2.
- Bumped from v0.5 → v0.7 with consolidated changes.

---

## 1. Executive Summary & Vision

InfoBase is an **opinionated, unified information storage and access system** designed specifically for small and medium businesses (SMBs). It functions as a "data fabric" that sits above and beyond traditional databases.

The core idea is to replace the fragmented, siloed reality most SMBs live with today:

- QuickBooks (or similar) for financials
- HubSpot / CRM tools
- Obsidian / Notion / loose Markdown notes
- Meeting recordings and transcripts
- Google Sheets and other ad-hoc data

With **one governed, agent-ready platform** that combines:

- A purpose-built financial ledger (general ledger style)
- Native rich Markdown notebook capabilities
- Other business data sources
- **Consistent RBAC and governance applied uniformly across everything**

The end state enables **powerful AI agents and "business super intelligences"** to operate safely and effectively on the company's full information set without the usual problems of context fragmentation, permission leakage, or governance gaps.

**Key Differentiator for SMBs**: Make excellent data governance *easy by default* instead of something that requires enterprise-grade IT resources. Reduce the terror of "AI will leak our data" while unlocking real productivity gains.

---

## 2. Problem Statement

Current state for most SMBs:

1. **Data Silos Everywhere**  
   Information about the business is scattered across 10–20+ disconnected sources. AI agents struggle to reliably "connect the dots" because each tool has its own data model, API quirks, and access patterns.

2. **Governance is Broken or Non-Existent Outside Databases**  
   - You can do proper role-based access control in QuickBooks.  
   - You cannot do it meaningfully in Obsidian notebooks or meeting recordings.  
   - Result: Either employees have overly broad access (including to sensitive financials), or companies are too scared to let AI touch anything real.

3. **Role Mapping Nightmare for AI Agents**  
   When an AI agent talks to QuickBooks, HubSpot, and notes, there is no clean way to translate "this user is a Sales Rep" into the correct permissions in each system. This makes safe agent deployment extremely difficult.

4. **SaaS Fatigue & Lock-in**  
   SMBs generally dislike managing multiple vertical SaaS tools. They use QuickBooks because their accountant likes it, HubSpot because it was free or recommended, etc. There is strong desire to consolidate but no good path to do so without massive migration pain.

5. **AI Adoption Paralysis**  
   Many SMBs either:
   - Throw everything into AI tools with weak/no governance (high risk), or
   - Avoid meaningful AI usage entirely out of fear.

---

## 3. Core Objectives

1. Create a single **opinionated information base** that combines financial records, Markdown notes, CRM-like data, and other sources under one roof.
2. Apply **consistent, fine-grained RBAC and governance** across *all* data types from day one.
3. Provide **simple, high-quality migration paths** out of QuickBooks, CRMs, Google Sheets, and unstructured sources.
4. Offer **efficient, permission-aware CLI interfaces** optimized for AI agents (preferred over MCP for token efficiency and security model simplicity).
5. Enable reliable, powerful AI agents that can work across the entire business context while automatically respecting permissions.
6. Make the system **simple enough for SMBs** to adopt without dedicated data engineering or security teams.

---

## 4. Design Tenets & Architectural Constraints (Non-Negotiable)

These principles guide every technical decision:

### 4.1 Primary Implementation Language: Go (Golang)
- The entire system shall be written in **Go** as the primary (and initially only) language.
- Rationale: Excellent performance, simple concurrency model, strong tooling, great ecosystem for CLIs and networked services, and aligns with building reliable, maintainable systems that coding agents can effectively generate and maintain.
- All core components (storage engine, RBAC layer, CLI, import tools, agent runtime hooks) must be implemented in Go.

### 4.2 Build from the Ground Up — Custom Opinionated Storage
- We will **not** patch together pre-existing databases or layers (no Postgres + extensions, no Neo4j, no dedicated vector DB, no lakehouse, no "use X for this and Y for that").
- With modern coding agents, we can and should design and implement a **purpose-built, custom storage engine** from scratch that natively supports:
  - Financial ledger operations with strong consistency and accounting invariants
  - Rich Markdown notebook storage with linking, versioning, and graph navigation
  - Unified metadata and RBAC enforcement at the storage primitive level
  - Efficient indexing for both exact and semantic queries
- Goal: Maximum control, simplicity, and the ability to enforce "one way to do things" from the lowest layers up. We own the data model and access patterns completely.

### 4.3 The "Only One Way to Do Things" Principle
- **There shall always be only one canonical, well-defined way for agents and humans to interact with data.**
- The system **must not** expose raw or low-level query interfaces (SQL, graph query languages, direct vector search, ad-hoc scripting, etc.) to AI agents.
- All read, write, query, and mutation operations — especially by agents — must go through the **official CLI** (and later controlled, versioned SDKs or APIs that wrap the same semantics).
- **Rationale**: Allowing agents to "write their own SQL" or craft arbitrary queries leads to inevitable drift. Over time the database becomes messy, schemas get polluted with agent-generated artifacts, governance is bypassed, and the system loses its opinionated character. By enforcing a single, curated interface we maintain long-term cleanliness, predictability, and safety.
- This tenet is **more important** than feature richness in the early phases. When in doubt, choose the design that preserves a single interaction path.

### 4.4 Core Tenet: Blast Radius Control + Recoverability via Versioning (Agentic-Era Governance)

In the agentic era, the two most critical properties of good data governance are:

- **Blast Radius Control** — The damage any single action (by a human or, especially, an autonomous AI agent) can cause must be limited and containable.
- **Recoverability** — Any destructive, incorrect, or malicious sequence of actions must be trivial to undo, audit, and replay from a known-good state.

**Foundational Requirement**:  
**Everything in InfoBase must be version-controlled, recoverable, and replayable at the storage level.**

- Every mutation — whether a financial journal entry, a Markdown note edit, a CRM record change, a permission update, or a bulk import — must create an immutable, versioned event.
- The storage engine must support full history, point-in-time reconstruction, undo/replay of action sequences, and time-travel queries.
- "Oops" scenarios (an agent goes off the rails and performs a large number of destructive updates/deletes) must be fixable in minutes, not hours or days of manual cleanup.
- This capability must be a **first-class, non-bypassable property of the core storage primitives**, not a bolted-on feature or optional layer.
- Audit logging is automatic and complete because the version history *is* the audit trail.

**Implications for Design & Implementation**:
- The custom Go storage engine (Tenet 4.2) **shall be built as a Merkle-DAG** from day one. This provides native immutability, content-addressing, structural sharing, cryptographic integrity, and first-class versioning/replayability as core primitives.
- Every CLI write command produces a new immutable node in the DAG. The current system state is represented by one or more live root pointers.
- RBAC changes themselves must be versioned events (so permission mistakes can also be rolled back cleanly).
- High-impact operations may require explicit confirmation, multi-party approval, or sandboxed execution, but the fundamental safety net is always the ability to replay history from any prior root.
- Long-term history growth is managed via:
  - Named **period-close roots** (fiscal year, quarter, month-end) that capture immutable snapshots of closed periods + opening balances for the next period.
  - Lightweight named **snapshots** for arbitrary save points.
  - Configurable retention policies + background garbage collection (GC) of unreachable nodes.
- This principle reinforces the "Only One Way to Do Things" rule: the canonical history (the DAG) becomes the single source of truth for the entire system state. All agents and humans interact only through the CLI.

This tenet is non-negotiable. Any design that makes full recoverability difficult or expensive violates the core promise of safe agentic operation for SMBs.

---



## 5. Key Requirements

### 5.1 Data Model & Storage (Opinionated Core — Custom Built)

The storage layer shall be **custom-designed and implemented in Go from the ground up** (see Design Tenet 4.2). It is not a wrapper around existing database engines.

- **Financial / Ledger Component**  
  Purpose-built storage optimized for financial record keeping. **Double-entry bookkeeping shall be enforced as a hard invariant at the storage primitive level** for all journal entries and transactions.  
  - Opinionated default toward **accrual-basis accounting** with strong consistency rules (inspired by core GAAP principles: balanced debits/credits, proper classification, etc.).  
  - Provide **cash-basis compatibility / views** for simplicity, especially for very small SMBs or tax reporting.  
  - The engine must prevent any mutation (human or agent) that would violate double-entry balance or core accounting invariants.  
  - This makes the ledger feel like “QuickBooks done right” — reliable for agents while remaining flexible for SMB needs.  
  - Full versioning and recoverability apply to every ledger mutation (see Tenet 4.4).

- **Markdown Notebook Component**  
  First-class, native support for rich Markdown notes with:
  - Every entity in the system (ledger transactions, accounts, journal entries, CRM records, other notes, etc.) is **addressable via stable, globally unique Entity IDs**.
  - Native first-class **typed reference system**: References are bidirectional, versioned events with metadata (type/role, author, timestamp). The CLI provides the only safe way to create and traverse them.
  - Deep linking and cross-referencing between notes and any other entity type — with groundwork for Phase 2 full enforcement of RBAC on the referencing document + Row-Level Security (RLS) on referenced items.
  - Search, backlinks, and graph-style navigation
  - Full version history and diff capabilities
  - All stored with the same unified metadata and RBAC system as financial data

- **Extensible but Opinionated Data Types**  
  The core engine must support additional business entities (CRM-style contacts/deals, meeting metadata + transcripts, documents, structured tables from Sheets, etc.) while keeping a single, consistent interaction model. New types should be added through well-defined extension points rather than ad-hoc tables or collections.

- **Unified Storage Primitives (Merkle-DAG Foundation)**  
  - The engine is implemented as a **Merkle-DAG** (content-addressed, immutable nodes with structural sharing). Every entity (ledger transactions, notes, references, RBAC changes, etc.) is a first-class node with a stable, globally unique content-derived identifier.
  - Single storage engine that provides both exact/structured access and semantic/vector capabilities without bolting on separate systems.
  - Rich metadata (owner, sensitivity, department, source, timestamps, etc.) is a first-class citizen and applies uniformly to every record and note.
  - **Versioning, recoverability, and replayability are native properties of the Merkle-DAG** (see Design Tenet 4.4). Every CLI mutation creates a new immutable node. The live system state is represented by root pointers. Point-in-time reconstruction, undo, replay, and full audit history are trivial by walking or re-rooting the DAG.
  - RBAC, audit logging, and versioning are implemented at the lowest storage layer so they cannot be bypassed.
  - **Period-close and snapshot semantics**: The CLI supports operations such as `ledger close-period` that create immutable named roots capturing closed fiscal periods + clean opening balances for the next period. Lightweight snapshots provide arbitrary save points. Background GC removes unreachable historical nodes according to retention policy. This keeps long-term storage growth manageable while preserving full history for audit and recovery.

- All data operations by agents must ultimately flow through the canonical CLI interface (see 5.4). Direct low-level access is reserved for the core engine and administrative tooling only.

### 5.2 Role-Based Access Control (RBAC) — Unified Across Everything

This is one of the most important requirements.

- Define business-level roles once (e.g., Owner, Accountant, Sales Rep, Operations, Admin).
- These roles must automatically control access to:
  - Financial records (row/record level where needed)
  - Specific Markdown notes or folders
  - CRM entities
  - Meeting recordings / transcripts
  - Agent interaction history
- Fine-grained permissions (read, write, delete, export, agent-use) per data domain.
- "Safe by default" philosophy: New users/agents should have minimal access until explicitly granted.
- Full audit logging of all access and permission changes. **All RBAC changes themselves are versioned events** (so mistaken or malicious permission grants can be cleanly rolled back — see Tenet 4.4).
- Easy for non-technical SMB owners to understand and configure.

### 5.3 Data Import & Migration

**Phase 1 Priority: QuickBooks**

- Excellent support for exporting and importing from QuickBooks:
  - CSV export/import
  - IIF format
  - QBXML where available
  - Future: Direct API sync (read-heavy initially)
- Intelligent mapping from QuickBooks data model into InfoBase's opinionated ledger model.
- Preserve historical data and relationships.

**CRM Data**

- CSV import from HubSpot, Salesforce, Pipedrive, etc. (easy path).
- Later: Direct API connectors.

**Flexible / Unstructured Sources (LLM-Powered)**

- Provide CLI tools + LLM assistance to ingest "goofy" data sources:
  - Google Sheets (messy structure)
  - Loose CSVs
  - Exported notes from Obsidian/Notion
  - PDFs and documents
- The system should use LLMs (with guardrails) to:
  - Parse and understand the data
  - Map it into the opinionated model
  - Apply appropriate metadata and RBAC tags automatically or with minimal human guidance
- Goal: Make onboarding realistic even when data is messy.

**General Import Principles**
- Idempotent imports where possible
- Clear audit trail of what was imported when and by whom
- Ability to review and correct LLM-assisted mappings before final commit

### 5.4 Agent Interface — CLI as the *Only* Canonical Path (Enforced)

This section is directly derived from Design Tenet 4.3 ("Only One Way to Do Things").

**Core Rule**: AI agents (and human power users) **must not** be given the ability to write arbitrary SQL, graph queries, vector searches, or raw mutations. All interaction with the InfoBase storage engine happens exclusively through the official, versioned CLI (and later through controlled SDKs that expose the exact same semantics).

Every CLI mutation command **must** produce an immutable, versioned event that can be audited, undone, or replayed (see Design Tenet 4.4 on Blast Radius + Recoverability).

**Why this matters more than convenience**:
- Prevents long-term semantic and structural drift.
- Ensures RBAC and business invariants are always enforced uniformly.
- Keeps the database clean, predictable, and truly opinionated.
- Makes governance and auditing tractable.

**Rationale for CLI over alternatives**:
- **Token Efficiency** — Dramatically lower token usage than MCP-style or chatty protocol interfaces.
- **Permission Model** — Far easier to implement strong, auditable, role-based permissions on top of a CLI (via policy engines, signed commands, or integrated RBAC checks) than on more open interfaces.
- **Single Source of Truth for Behavior** — The CLI command surface *is* the contract. Agents learn one consistent way to work with financials, notes, and cross-domain data.

**Requirements for the CLI layer:**

- The CLI is the **primary and enforced interface** for all agent-driven data access and mutation.
- Commands must feel natural to both autonomous agents and human operators.
- Agents must be able to:
  - Perform precise financial/ledger operations while the engine automatically enforces accounting rules
  - Read, create, edit, and link Markdown notes with rich context and backlinks
  - Execute safe cross-domain queries and actions (e.g., "Find all clients with overdue invoices who were mentioned in Q2 meeting notes") — all filtered by the agent's current role
  - Never bypass RBAC — the CLI layer must reject any operation outside the authenticated role's permissions before it reaches storage
- Support both interactive (human) and non-interactive (agent) execution modes.
- Output must be available in clean machine-readable format (JSON by default for agents) as well as human-friendly text.
- Excellent discoverability: comprehensive `--help`, command categories, and examples so agents can explore capabilities without hallucinating commands.
- Every CLI command must be versioned and backward-compatible within major versions. Breaking changes require explicit migration support.

MCP (or any other protocol) is **not** planned as a primary interface. It may be evaluated later only as a thin, read-only compatibility layer if strong demand emerges — and even then it must delegate to the same canonical CLI semantics underneath.

Direct database access (bypassing the CLI) is reserved exclusively for:
- Core storage engine internals
- Administrative migration / recovery tooling
- Explicitly audited super-admin operations

This constraint is intentional and non-negotiable for maintaining long-term system health.

### 5.5 AI / Super Agent Capabilities

- Built-in RAG and semantic search over the *entire* unified dataset, with RBAC filters applied transparently before any retrieval.
- Agents should be able to maintain consistent, reliable context across financials + notes + CRM without manual stitching or fragile prompt engineering.
- Support for agent-driven workflows that span multiple data domains while staying within permission boundaries.
- Future capabilities (not Phase 1): Automated reporting, anomaly detection, workflow automation, proactive insights.

---

## 6. Non-Functional Requirements

| Category              | Requirement                                                                 | Priority |
|-----------------------|-----------------------------------------------------------------------------|----------|
| **Simplicity (SMB)**  | SMB owner or office manager should be able to set up core functionality in hours, not weeks. Minimal technical expertise required. | Critical |
| **Security**          | Encryption at rest and in transit. Strong audit logging. Easy path to common compliance needs. | Critical |
| **RBAC Usability**    | Permission model must be understandable by non-technical users. | Critical |
| **Performance**       | Fast queries and CLI responses even on typical SMB data volumes (tens to low hundreds of thousands of records + notes). | High |
| **Reliability**       | Financial data integrity is non-negotiable. Strong consistency and validation for ledger operations. | Critical |
| **Extensibility**     | Clear extension points for additional data sources and custom agent tools. | Medium |
| **Cost**              | Pricing and resource usage must be appropriate for SMB budgets. | High |
| **Migration**         | Migration tools must be reliable and provide clear rollback / verification steps. | High |

---

## 7. Suggested Phased Delivery

**Phase 1 — Foundation (MVP)**
- Core opinionated storage: Ledger + Markdown notebook
- Unified RBAC model applied to both
- QuickBooks import (CSV + basic mapping)
- Basic but powerful CLI for agents (core query + note operations)
- Simple role management UI / config

**Phase 2 — Expansion**
- CRM / Google Sheets import with LLM assistance
- Cross-domain queries and richer CLI commands
- Meeting metadata / transcript support
- Improved agent RAG with governance
- **Cross-entity referencing with enforced governance (RBAC + RLS)**: Every entity type must be able to reference any other entity type (e.g., a Markdown note referencing a specific set of ledger transactions, journal entries, CRM records, or other notes). The system must enforce:
  - Full RBAC on the referencing document/note itself
  - Row-Level Security (RLS) on the referenced items (e.g., a user/agent who can read the Markdown note must *also* have permission to see the specific ledger transactions it links to; unauthorized references are either hidden, redacted, or blocked at query time)
  - This capability must be native to the storage engine and CLI so that agents can safely create, traverse, and query rich cross-linked information graphs without ever bypassing permissions.

**Phase 3 — Intelligence Layer**
- Advanced "super agent" capabilities
- Workflow automation
- Proactive insights and reporting agents
- Deeper integrations and self-service migration tools

---

## 8. Open Questions & Decisions Needed

1. **Data Model Details**
   - ~~What exact accounting model should the custom ledger follow? (US GAAP basics? Simpler cash vs. accrual? How strictly should double-entry be enforced at the storage primitive level?)~~  
     **Resolved (v0.5)**: Double-entry enforced as hard storage invariant. Opinionated default = accrual-basis + cash-basis views. Engine prevents any violating mutation.
   - ~~How should notes and other entities link to ledger records (invoices, customers, journal entries, etc.) in the unified store?~~  
     **Resolved (v0.5)**: Every entity is addressable via stable, globally unique Entity IDs. Native first-class typed reference system (bidirectional, versioned events with metadata). CLI is the single canonical way to create/traverse governed references. This provides the foundation for Phase 2 RBAC + RLS enforcement on cross-entity links.

2. **Custom Storage Engine Design (Ground-Up)**
   - What are the minimal core storage primitives needed to support ledger + Markdown notebook + extensible entities with unified RBAC and metadata from day one?
   - How should we design the on-disk format and indexing strategy for a good balance of write performance, query performance, and simplicity (especially for SMB data volumes)?
   - **How do we implement first-class versioning, immutability/event-sourcing, point-in-time recovery, and replayability as core primitives** (see new Tenet 4.4)? Should we use an append-only log + materialized views, a Merkle-DAG structure, or another pattern?  
     **Resolved (v0.7)**: Build the custom storage engine as a **Merkle-DAG** (content-addressed immutable nodes with structural sharing). This was chosen over pure append-only log because it provides stronger long-term storage efficiency, natural cryptographic integrity, easier cross-entity referencing, and a mental model the team already understands deeply from Git. Long-term bloat is managed via named period-close roots (fiscal year/quarter/month-end), lightweight snapshots, configurable retention, and background GC of unreachable nodes — directly analogous to Git tags + `git gc`. Period-close operations create immutable roots that capture closed periods + opening balances for the next period, giving clean accounting boundaries while preserving full replayability.
   - How do we bake semantic/vector search capabilities into the same engine without bolting on a separate system while preserving full versioning?
   - What internal interfaces should the CLI use to talk to the storage engine (to keep the "only one way" boundary clean)?

3. **CLI Surface Area & Command Design**
   - What is the initial minimal, high-value set of CLI commands for agents? (Focus on the commands that deliver the most "super agent" leverage while staying simple.)
   - How should command output and error handling be structured so agents can reliably parse results and recover from permission or validation errors?

4. **LLM Usage in Import & Onboarding**
   - How much autonomy should the LLM have during messy data import (Google Sheets, loose notes, etc.) vs. human-in-the-loop review and approval?
   - What guardrails and audit mechanisms should exist around LLM-assisted mapping into the opinionated model?

5. **Deployment Model**
   - Self-hosted binary only? Cloud SaaS offering? Hybrid / air-gapped support?

6. **Binary / Rich Media Handling**
   - Should actual meeting audio/video files be stored inside InfoBase (with full RBAC) or should we store only metadata + transcription and reference external secure storage?

7. **Pricing, Packaging & Go-to-Market**
   - How should this be packaged and priced for SMBs (per user, per company, usage-based, etc.)?
   - What is the migration story and pricing incentive to move off QuickBooks + other tools?

---

## 9. Success Metrics (Future)

- Time for an SMB to fully migrate core QuickBooks + notes data and have first agent running safely.
- Reduction in "data access anxiety" reported by SMB owners.
- Number of cross-domain queries successfully executed by agents without manual context stitching.
- Adoption rate of CLI by both human power users and autonomous agents.

---

**End of Document**

**Version History**
- v0.1 (June 28, 2026) — Initial capture of vision, problems, objectives, and requirements.
- v0.2 (June 28, 2026) — Added Design Tenets section (Go language, ground-up custom storage engine, "Only One Way to Do Things" principle). Updated Data Model, Agent Interface, and Open Questions to align with these non-negotiable architectural constraints.
- v0.3 (June 28, 2026) — Added cross-entity referencing with RBAC + RLS enforcement as a Phase 2 expansion requirement (including explicit example of Markdown notes referencing ledger transactions). Strengthened Data Model section to foreshadow governed cross-linking.
- v0.4 / v0.5 (June 28, 2026) — Resolved two major Phase 1 data model questions: (1) Double-entry bookkeeping enforced at storage primitive level with accrual default + cash-basis views. (2) Every entity addressable via stable Entity IDs + native first-class typed reference system. Updated Sections 5.1 and 8 accordingly. Consolidated into v0.5.
- v0.6 / v0.7 (June 28, 2026) — Major architectural decision: Committed to **Merkle-DAG** as the core storage pattern for the custom Go engine (with structural sharing, content-addressing, and Git-style versioning). Added comprehensive long-term bloat management strategy using period-close roots, named snapshots, retention policies, and background GC. Updated Tenet 4.4, Section 5.1, and resolved the versioning open question in Section 8. This choice was made because coding agents remove implementation-time concerns and Merkle-DAG best satisfies blast-radius control, recoverability, and future composability while aligning with the team's deep Git familiarity.

This PRD now reflects the team's decision to build a purpose-built system in Go from the ground up, with the CLI as the single enforced interaction path for agents. It is ready to be fed to coding agents for:

- High-level architecture design of the custom storage engine
- RBAC model and enforcement strategy at the storage layer
- Initial CLI command surface design (with strong emphasis on the "one way" contract)
- Technical spike planning for the ledger + Markdown primitives

Next recommended artifacts:
1. Architecture Decision Record (ADR) for the custom storage engine primitives
2. Detailed CLI command taxonomy and interaction contract
3. RBAC domain model and enforcement points diagram

The team should treat the "Only One Way to Do Things" tenet as a first-class constraint during all design and implementation work.