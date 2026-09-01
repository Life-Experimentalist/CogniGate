# GW-2 — Capability aliases

**Status:** Specified · **Plane:** Data + admin · **Depends on:** GW-1

## Motivation

Clients that hardcode model ids inherit every provider's churn. The
contract downstream applications actually want is *intent*: "give me the
fast cheap model", "give me the best reasoning model", "transcribe this".
Aliases decouple every client from the model catalog: admins define the
policy once, per tenant, and new models flow into service with zero
client releases.

## Behavioral requirements

### Alias definition

- An alias is a per-tenant named rule. Its schema (managed via GW-6):

  ```json
  {
    "name": "fast",
    "selector": {
      "capabilities": ["chat"],
      "providers_preferred": ["groq", "openai"],
      "cost_tier": "low",
      "min_context_window": 16000
    },
    "pin": null,
    "fallback_chain": ["balanced"]
  }
  ```

- `name` MUST match `^[a-z][a-z0-9_-]{1,63}$` and MUST NOT equal any
  model id in the live catalog; both alias save and catalog refresh MUST
  detect collisions (save is rejected with 409
  `error.code = "alias_collides_with_model"`; a collision introduced by a
  catalog refresh keeps the alias authoritative and logs a warning).
- `pin` MAY name a concrete model id, overriding the selector until the
  pin is cleared or the model disappears (then selector resolution
  resumes). Pinning is how admins do controlled rollouts.
- Every deployment MUST seed the four **well-known aliases** for each new
  tenant: `fast`, `balanced`, `best`, `transcribe`. Admins may edit or
  delete them; the names are convention, not reserved words.

### Resolution

- When a data-plane request's `model` field names an alias for the
  calling tenant, CogniGate MUST resolve it against the **live catalog
  (GW-1)** at request time. Resolution order: `pin` if set and alive,
  else selector match (filter by capabilities → filter by
  `min_context_window` → order by provider preference → order by cost
  tier), deterministic given the same catalog snapshot.
- If the `model` field matches no alias, it MUST be treated as a concrete
  model id (pass-through). Alias lookup happens first; the collision rule
  above guarantees the two namespaces are disjoint.
- If an alias resolves to nothing (empty selector result, no live pin,
  and no usable fallback), the request MUST fail with 404
  `error.code = "alias_unresolvable"` and the alias MUST be flagged
  degraded in GW-5/GW-6.
- The response MUST report the concrete resolution: the
  `X-CogniGate-Served-By: <provider>/<model-id>` header and the `model`
  field of the response body carry the **actual model**, never the alias.
  (Truthfulness rule shared with GW-3.)
- New models that match a selector MUST become eligible on the catalog
  refresh that discovers them — no config change, no restart, no client
  change.

### Interaction with routing

- An alias resolution feeds the routing rule / fallback machinery of
  GW-3 exactly as a concrete model id would. An alias's own
  `fallback_chain` (list of aliases or model ids) is consulted only when
  the resolved target's chain is exhausted.

## Configuration surface

| Key                                   | Default | Meaning |
| ------------------------------------- | ------- | ------- |
| `aliases.seed_defaults`               | `true`  | Seed `fast`/`balanced`/`best`/`transcribe` on tenant creation |
| `aliases.cost_tiers`                  | `low, medium, high` | Ordered tier labels admins can reference |
| Per-alias fields                      | —       | See schema above; managed via GW-6, not files |

## Acceptance criteria

- **GW-2.AC-1** — `POST /v1/chat/completions` with `"model": "fast"`
  returns 200; the response body `model` and the `X-CogniGate-Served-By`
  header name a concrete catalog model, not `fast`.
- **GW-2.AC-2** — Creating an alias whose name equals a live model id
  returns 409 `alias_collides_with_model`.
- **GW-2.AC-3** — With a (mock) provider adding a cheaper model matching
  the `fast` selector, after a catalog refresh the same client request
  resolves to the new model with no configuration or client change.
- **GW-2.AC-4** — Pinning an alias via GW-6 makes subsequent requests
  serve the pinned model; clearing the pin restores selector resolution.
- **GW-2.AC-5** — An alias whose selector matches nothing returns 404
  `alias_unresolvable`, and `GET /v1/health` lists the alias as degraded.
- **GW-2.AC-6** — Aliases are tenant-scoped: tenant A's `fast` and tenant
  B's `fast` may resolve differently; tenant A cannot see or use an alias
  defined only for tenant B.
- **GW-2.AC-7** — `GET /v1/models` lists each alias with
  `cognigate.alias == true` and its current `resolves_to`.

## Non-goals

- No automatic quality-based selection (latency probes, eval scores).
  Selection is policy-driven by admin-defined rules only; anything
  smarter is a future, separately-specified feature.
- No request-content inspection to "guess" the right alias — clients
  choose the alias; CogniGate never reads prompts to route (see GW-14).
- Alias names carry no built-in semantics; `fast` is a convention that
  admins give meaning to.
