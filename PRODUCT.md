# Product

## Register

product

## Platform

web

## Users

External coding agents are the primary users. They deploy, inspect, invoke, and diagnose automations through a stable API without depending on a visual workflow editor. Operators are the secondary users: they need a fast workspace for understanding system state, intervening safely, and seeing the same facts and actions available to agents.

## Product Purpose

Werkt is one organized control plane for AI-authored code automations triggered by schedules, webhooks, email, ntfy, and future event sources. It turns every trigger into a durable language-neutral event and runs immutable Python, Rust, Go, or other executable automation revisions through one protocol. Success means agents can manage the full lifecycle predictably while operators can diagnose failures without bypassing or reverse-engineering the API.

## Positioning

The durable, language-neutral meeting point between external events, AI-authored code, and isolated execution.

## Brand Personality

Direct, assured, and useful. Werkt should feel like a well-made instrument: concise under normal operation, precise when something fails, and lightly human without turning operational work into a joke. The name supplies enough personality; interface copy does not need to perform cleverness.

## Anti-references

- Node-canvas workflow builders that make code look secondary.
- Generic SaaS KPI dashboards made from interchangeable metric cards.
- Cyberpunk agent consoles with neon gradients, glass panels, or ornamental terminals.
- Interfaces that invent friendly but vague abstractions instead of showing revisions, triggers, runs, and errors directly.
- AI-generated sameness: inflated radii, floating cards, purple-blue gradients, and decorative automation diagrams.

## Design Principles

- **The API is the product.** The workspace consumes the same public management contract as an external agent; it never creates a privileged second control path.
- **Show system truth.** Prefer exact lifecycle state, revision IDs, trigger schedules, attempts, logs, and results over synthesized scores or decorative charts.
- **Manage by exception.** Healthy automations should be scannable; failures and blocked configuration should draw attention and provide an immediate diagnostic path.
- **Keep code first.** Organize and operate automations without replacing source packages or manifests with a visual DSL.
- **Make risky actions legible.** Explain scope, preserve idempotency, attribute mutations, and provide visible in-progress, success, and error feedback.

## Accessibility & Inclusion

The web workspace targets WCAG 2.2 AA. Every workflow must be keyboard-operable, status must never rely on color alone, focus must remain visible, touch targets must be usable, motion must respect reduced-motion preferences, and dense operational data must reflow without hiding core actions.
