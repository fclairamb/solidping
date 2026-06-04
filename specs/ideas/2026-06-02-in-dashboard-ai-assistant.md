# In-Dashboard AI Assistant (ChatOps)

## Question

OpenStatus shipped a built-in **chat assistant** ([changelog](https://www.openstatus.dev/changelog/chat-assistant)):
an "Assistant" in the dashboard sidebar that answers questions about the workspace,
drafts status reports, and inspects monitors in natural language. Should SolidPing
build the equivalent?

## What the OpenStatus feature is

An in-dashboard AI chat with two design choices that matter:

- **Every tool call renders inline as a table or change diff** before it executes —
  the user sees exactly what the assistant is about to do.
- **All mutations are written to the audit log, attributed to the user** who triggered them.

Their framing is "clickops → chatops". Crucially, OpenStatus *already had* a CLI/IaC
path and an MCP server — the chat assistant brings that capability **natively into the
dashboard** instead of requiring an external MCP client (no Slack, no MCP client, no
context-switching).

## Why this fits SolidPing especially well

The decisive fact: **SolidPing already has a production-grade MCP server**
(`server/internal/mcp/`, 40+ org-scoped tools, session management, plus a `diagnoseCheck`
tool that already bundles recent results / incidents / events). That is normally the hard
~70% of this feature — the tool layer, auth scoping, and data access already exist and are
already designed for an LLM to drive.

So this isn't a from-scratch build. SolidPing already has REST (clickops) and MCP
(bring-your-own-client); the chat assistant is the **missing third leg** — a native chat UI
plus a server-side LLM loop that calls the *existing* MCP tools.

The domain fit is genuinely strong. Monitoring is one of the better chat use cases:
*"why is this check flapping?"*, *"draft a status-page update for the last incident"*,
*"diagnose check X"* — all map cleanly onto tools we already ship.

## The real costs

1. **New hard dependency on an LLM provider.** Today there is *zero* OpenAI/Anthropic SDK
   in the repo. SolidPing's identity is a self-hostable single binary (Postgres *or* SQLite,
   no external queue). "You now need an LLM API key" is a philosophical shift. It must be
   **off by default, bring-your-own-key, and provider-pluggable** so self-hosters aren't
   forced into it and we don't silently eat inference cost.

2. **Mutations via chat are the risky part.** Reuse OpenStatus's mitigations exactly
   (confirm-diff UX + audit-log attribution) and add **prompt-injection defense**: check
   `output` and result bodies are attacker-influenced text, and our MCP tools include
   writes/deletes. An LLM reading a malicious check output and then calling `deleteCheck` is
   a concrete threat. Writes stay gated behind explicit user confirmation — never
   auto-execute.

3. **Streaming plumbing.** Interactive chat wants SSE for token streaming + inline tool
   rendering. The current request model is plain request/response; the background-job system
   doesn't fit interactive chat.

4. **Marginal value for power users** who already use the MCP path — the real win is the
   majority who will never configure an MCP client.

## How to ship it

- **Phase 1 (low risk, high value):** read-only + diagnostics chat. Lean on `diagnoseCheck`
  and the read tools. No mutations. Delivers most of the value, dodges most of the risk.
- **Phase 2:** mutations behind inline diff + confirm + audit log, exactly like OpenStatus.
- **Reuse the existing MCP tools server-side** — do not build a parallel tool layer.
- Provider abstraction, **disabled by default**, per-org API key stored in `Parameter`
  (secret params are already encrypted at rest).

## Verdict

Worth doing, and cheaper for SolidPing than for most projects because the MCP foundation is
already there. Not urgent over core monitoring-reliability work, but a strong differentiator
at low incremental cost given the architecture. The things to be deliberate about are **not**
feasibility — they are the LLM-dependency philosophy (BYO-key, off by default) and the
write-path safety (confirm-diff, audit log, prompt-injection defense).

## References

- OpenStatus chat assistant changelog: https://www.openstatus.dev/changelog/chat-assistant
- OpenStatus CLI / clickops→IaC blog: https://www.openstatus.dev/blog/introducing-openstatus-cli
- Existing MCP server: `server/internal/mcp/` (handler, 40+ tools, `diagnoseCheck`)
