"""Generate openapi.yaml from the gateway's actual route table and Go types.

Run from the repository root:  python scripts/gen_openapi.py

Every path here is reconciled against app.GetRoutes() by
gateway/internal/server/openapi_test.go, so a route added without a
corresponding entry fails the build.
"""

import io
import sys
from collections import OrderedDict

NL = chr(10)

import yaml


# --- YAML emission ----------------------------------------------------------

class Literal(str):
    """A string emitted as a YAML block scalar."""


def _literal(dumper, data):
    return dumper.represent_scalar("tag:yaml.org,2002:str", str(data), style="|")


def _ordered(dumper, data):
    return dumper.represent_mapping("tag:yaml.org,2002:map", data.items())


yaml.add_representer(Literal, _literal)
yaml.add_representer(OrderedDict, _ordered)


class Dumper(yaml.Dumper):
    """Writes every value out in full rather than aliasing repeats.

    Several objects here are shared rather than copied — the tenant path
    parameter, the pagination parameters, the security requirements, the usage
    totals spliced into two schemas. PyYAML would emit the second and later
    appearances of each as `*id001` anchors, which every parser resolves and no
    reader enjoys. A published contract is read by people too.
    """

    def ignore_aliases(self, data):
        return True


def D(*pairs):
    return OrderedDict(pairs)


def ref(name):
    return {"$ref": "#/components/schemas/" + name}


def rref(name):
    return {"$ref": "#/components/responses/" + name}


def href(name):
    return {"$ref": "#/components/headers/" + name}


# --- schema helpers ---------------------------------------------------------

def obj(props, required=None, extra=None):
    s = D(("type", "object"))
    if required:
        s["required"] = required
    s["properties"] = OrderedDict(props)
    if extra:
        s.update(extra)
    return s


def p(typ, desc=None, **kw):
    s = D(("type", typ))
    if desc:
        s["description"] = desc
    s.update(kw)
    return s


def dt(desc=None):
    return p("string", desc, format="date-time")


def arr(items, desc=None):
    s = D(("type", "array"))
    if desc:
        s["description"] = desc
    s["items"] = items
    return s


def listing(item_ref, desc):
    """The GW-6 list envelope: {object, data, has_more}."""
    return obj(
        [
            ("object", p("string", None, const="list")),
            ("data", arr(item_ref)),
            (
                "has_more",
                p("boolean", "True when a further page exists; pass the last item's "
                             "`id` as `?after=` to fetch it."),
            ),
        ],
        required=["object", "data", "has_more"],
        extra={"description": desc},
    )


# --- schemas ----------------------------------------------------------------

SCHEMAS = OrderedDict()

SCHEMAS["Error"] = obj(
    [
        (
            "error",
            obj(
                [
                    ("message", p("string", "Human-readable explanation. Never contains "
                                            "prompt or completion content.")),
                    (
                        "type",
                        p("string", "OpenAI's error family, reused so an existing SDK "
                                    "branches on it unchanged.",
                          enum=["invalid_request_error", "authentication_error",
                                "rate_limit_error", "api_error"]),
                    ),
                    (
                        "code",
                        p("string", "The closed CogniGate code registry. A client that "
                                    "branches on anything should branch on this.",
                          enum=[
                              "invalid_api_key", "wrong_plane",
                              "invalid_request", "fallback_duplicate_model",
                              "capture_ttl_too_long", "unsupported_parameter",
                              "insufficient_scope",
                              "model_not_found", "alias_unresolvable",
                              "not_supported", "resource_not_found",
                              "alias_collides_with_model", "conflict",
                              "request_too_large",
                              "rate_limited", "concurrency_exceeded",
                              "quota_exceeded", "budget_exceeded",
                              "upstream_exhausted", "response_too_large",
                              "upstream_error",
                              "service_unavailable",
                              "gateway_timeout",
                              "upstream_stream_stalled",
                          ]),
                    ),
                    (
                        "param",
                        D(("type", ["string", "null"]),
                          ("description",
                           "The offending field, or null when the failure is not "
                           "attributable to one. Null rather than omitted, because "
                           "OpenAI clients expect the key to be present.")),
                    ),
                    ("request_id", p("string", "Echoes X-CogniGate-Request-Id.")),
                    (
                        "attempts",
                        arr(ref("Attempt"),
                            "Present on `upstream_exhausted` only (GW-3.AC-5): the "
                            "cascade as it was tried, in order."),
                    ),
                ],
                required=["message", "type", "code", "param"],
            ),
        ),
    ],
    required=["error"],
    extra={"description": Literal(
        "The GW-7 error envelope. Every failure on either plane leaves the "
        "gateway in this shape, so stock OpenAI error handling parses a "
        "CogniGate error without a special case.\n")},
)

SCHEMAS["Attempt"] = obj(
    [
        ("provider", p("string")),
        ("model", p("string")),
        ("failure", p("string", "Classification of why this candidate failed, from a "
                                "closed vocabulary. Never the upstream's own message.")),
        ("status", p("integer", "The upstream's HTTP status; absent when there was "
                                "never a response to take one from.")),
    ],
    required=["provider", "model", "failure"],
)

SCHEMAS["ChatRequest"] = obj(
    [
        ("model", p("string", "A model id, an alias, or a routing-rule match. Resolved "
                              "per tenant (GW-1, GW-2, GW-3).")),
        ("messages", arr(ref("ChatMessage"))),
        ("stream", p("boolean", "When true the response is `text/event-stream`. Strictly "
                                "boolean: `1` is refused.")),
    ],
    required=["model", "messages"],
    extra={
        "additionalProperties": True,
        "description": Literal(
            "The OpenAI chat-completions request. Only the fields the gateway itself "
            "reads are described; everything else is forwarded to the provider "
            "untouched, which is what keeps an existing SDK working against this "
            "endpoint.\n"),
    },
)

SCHEMAS["ChatMessage"] = obj(
    [
        ("role", p("string", None, enum=["system", "user", "assistant", "tool"])),
        ("content", p("string")),
    ],
    required=["role"],
    extra={"additionalProperties": True},
)

SCHEMAS["ChatResponse"] = obj(
    [
        ("id", p("string")),
        ("object", p("string", None, const="chat.completion")),
        ("created", p("integer")),
        ("model", p("string", "The model that actually served the request, which is not "
                              "necessarily the one asked for — see "
                              "X-CogniGate-Fallback-Depth.")),
        ("choices", arr(D(("type", "object"), ("additionalProperties", True)))),
        ("usage", ref("Usage")),
    ],
    extra={
        "additionalProperties": True,
        "description": Literal(
            "The provider's response, forwarded. The gateway does not rewrite the "
            "body; what it adds is carried in the X-CogniGate-* headers.\n"),
    },
)

SCHEMAS["Usage"] = obj(
    [
        ("prompt_tokens", p("integer")),
        ("completion_tokens", p("integer")),
        ("total_tokens", p("integer")),
    ],
    extra={"additionalProperties": True},
)

SCHEMAS["ModelObject"] = obj(
    [
        ("id", p("string")),
        ("object", p("string", None, const="model")),
        ("created", p("integer")),
        ("owned_by", p("string")),
        ("cognigate", ref("ModelExtensions")),
    ],
    required=["id", "object", "created", "owned_by", "cognigate"],
    extra={"description": Literal(
        "One catalog entry in OpenAI's `model` shape, with everything CogniGate "
        "knows beyond that namespaced under `cognigate` so a client parsing the "
        "OpenAI fields is unaffected by it.\n")},
)

SCHEMAS["ModelExtensions"] = obj(
    [
        ("provider", p("string")),
        ("context_window", p("integer")),
        ("max_output_tokens", p("integer")),
        ("capabilities", arr(p("string"))),
        ("input_cost_per_mtok", p("number")),
        ("output_cost_per_mtok", p("number")),
        ("deprecated", p("boolean")),
        ("discovered_at", dt()),
        ("alias", p("boolean", "True when this entry is an alias rather than a "
                               "provider-discovered model.")),
        ("resolves_to", p("string", "For an alias, the model id it currently selects.")),
    ],
)

SCHEMAS["ModelList"] = obj(
    [
        ("object", p("string", None, const="list")),
        ("data", arr(ref("ModelObject"))),
    ],
    required=["object", "data"],
)

# The totals are spelled out at every point they appear rather than referenced,
# because that is how they appear on the wire: Go embeds store.UsageTotals, so
# its fields sit at the top level of the response rather than under a key.
TOTALS = [
    ("requests", p("integer")),
    ("prompt_tokens", p("integer")),
    ("completion_tokens", p("integer")),
    ("total_tokens", p("integer")),
    ("cost_usd", p("number")),
]
TOTALS_REQUIRED = ["requests", "prompt_tokens", "completion_tokens", "total_tokens",
                   "cost_usd"]

SCHEMAS["UsageLimit"] = obj(
    [
        ("scope", p("string", None, enum=["tenant", "key"])),
        ("window", p("string", None, enum=["day", "month"])),
        ("unit", p("string", None, enum=["tokens", "cost"])),
        ("cap", p("number")),
        ("soft_threshold_pct", p("integer")),
        ("consumed", p("number")),
        ("remaining", p("number")),
        ("resets_at", dt()),
        ("state", p("string", None, enum=["ok", "soft-exceeded", "hard-exceeded"])),
    ],
    required=["scope", "window", "unit", "cap", "soft_threshold_pct", "consumed",
              "remaining", "resets_at", "state"],
)

SCHEMAS["UsageResponse"] = obj(
    [
        ("object", p("string", None, const="usage")),
        ("window", p("string", None, enum=["day", "month"])),
        ("since", dt()),
        ("until", dt()),
    ] + TOTALS + [
        ("state", p("string", "The worst state across `limits`.",
                    enum=["ok", "soft-exceeded", "hard-exceeded"])),
        ("limits", arr(ref("UsageLimit"),
                       "Every quota slot that applies, tenant and key alike. Empty "
                       "when nothing is capped.")),
    ],
    required=["object", "window", "since", "until", "state",
              "limits"] + TOTALS_REQUIRED,
)

SCHEMAS["UsageBucket"] = obj(
    [("key", p("string", "The value of the grouping dimension for this row."))] + TOTALS,
    required=["key"] + TOTALS_REQUIRED,
)

SCHEMAS["UsageBreakdown"] = obj(
    [
        ("object", p("string", None, const="usage_breakdown")),
        ("window", p("string", None, enum=["day", "month"])),
        ("group_by", p("string", None,
                       enum=["model", "provider", "key", "client_request_id"])),
        ("since", dt()),
        ("until", dt()),
        ("data", arr(ref("UsageBucket"), "Ordered by spend, descending.")),
        ("truncated", p("boolean",
                        "True when there were more than 200 buckets and the cheapest "
                        "were dropped.")),
    ],
    required=["object", "window", "group_by", "since", "until", "data", "truncated"],
)

SCHEMAS["HealthReport"] = obj(
    [
        ("status", p("string", None, enum=["ok", "degraded", "unavailable"])),
        ("gateway", obj([("version", p("string")),
                         ("uptime_seconds", p("integer"))],
                        required=["version", "uptime_seconds"])),
        ("store", ref("ComponentHealth")),
        ("catalog", ref("CatalogHealth")),
        ("providers", arr(ref("ProviderHealth"))),
        ("aliases", arr(ref("NameState"))),
        ("rules", arr(ref("NameState"))),
        ("quota", obj([("state", p("string", None,
                                   enum=["ok", "soft-exceeded", "hard-exceeded"]))],
                      required=["state"])),
        ("checked_at", dt()),
    ],
    required=["status", "gateway", "store", "catalog", "providers", "aliases", "rules",
              "quota", "checked_at"],
    extra={"description": Literal(
        "The GW-5 view: what this deployment can currently reach, how fresh what it "
        "knows is, and which of the tenant's configured names still resolve.\n")},
)

SCHEMAS["ComponentHealth"] = obj(
    [("kind", p("string")), ("reachable", p("boolean")), ("error", p("string"))],
    required=["kind", "reachable"],
)

SCHEMAS["CatalogHealth"] = obj(
    [
        ("models", p("integer")),
        ("age_seconds", p("integer")),
        ("state", p("string", None, enum=["fresh", "stale"])),
        ("stale", p("boolean")),
        ("fetched_at", dt()),
    ],
    required=["models", "age_seconds", "state", "stale"],
)

SCHEMAS["ProviderHealth"] = obj(
    [
        ("provider", p("string")),
        ("enabled", p("boolean")),
        ("models", p("integer")),
        ("breaker", p("string", None, enum=["closed", "open", "half-open"])),
        ("breaker_until", dt()),
        ("breakers", arr(obj([("model", p("string")),
                              ("breaker", p("string", None, enum=["open", "half-open"])),
                              ("breaker_until", dt())],
                             required=["model", "breaker"]),
                         "Per-model breakers, present only for the models whose own "
                         "breaker is not closed.")),
        ("catalog", obj([("age_seconds", p("integer")),
                         ("state", p("string", None, enum=["fresh", "stale"]))],
                        required=["age_seconds", "state"])),
        ("error", p("string")),
    ],
    required=["provider", "enabled", "models", "breaker", "catalog"],
)

SCHEMAS["NameState"] = obj(
    [("name", p("string", "The alias name, or the routing rule's match pattern.")),
     ("state", p("string", None, enum=["ok", "degraded"])),
     ("resolves_to", p("string", "What a request naming this would be served by "
                                 "today. Absent when nothing resolves.")),
     ("reason", p("string", "Why it is degraded. Absent while it is serving."))],
    required=["name", "state"],
    extra={"description": "An alias or routing rule and whether it still resolves."},
)

SCHEMAS["Meta"] = obj(
    [
        ("name", p("string")),
        ("version", p("string", "Semver. GW-9 makes this a value a client may parse.")),
        ("api_version", p("string", "The major the URL prefixes carry.", const="v1")),
        ("capabilities", arr(p("string"))),
        ("endpoints", arr(p("string"), "The data-plane paths this deployment serves.")),
        ("limits", ref("MetaLimits")),
        ("object", p("string", None, const="meta")),
        ("mode", p("string", None, enum=["dev", "server"])),
        ("store", p("string")),
        ("planes", D(("type", "object"),
                     ("additionalProperties", p("string")),
                     ("description", "Plane name to URL prefix."))),
        ("features", D(("type", "object"),
                       ("additionalProperties", p("boolean")),
                       ("description",
                        "Optional behaviour and whether this deployment has it on. "
                        "GW-9 makes this the supported way to feature-detect."))),
    ],
    required=["name", "version", "api_version", "capabilities", "endpoints", "limits",
              "object", "mode", "store", "planes", "features"],
)

SCHEMAS["MetaLimits"] = obj(
    [
        ("max_request_bytes", p("integer")),
        ("max_response_bytes", p("integer")),
        ("request_timeout_seconds", p("integer")),
        ("stream_idle_timeout_seconds", p("integer")),
        ("max_concurrent_per_key", p("integer")),
        ("max_fallback_depth", p("integer")),
        ("requests_per_second", p("integer")),
        ("burst_capacity", p("integer")),
    ],
)

SCHEMAS["AdminMeta"] = obj(
    [
        ("scope", p("string", "`root`, or `tenant:<id>` for a tenant-scoped key.")),
        ("events", arr(p("string"), "Every event type a webhook may subscribe to.")),
    ],
    required=["scope", "events"],
    extra={"additionalProperties": True},
)

SCHEMAS["TenantLimits"] = obj(
    [
        ("max_request_bytes", p("integer")),
        ("request_timeout_seconds", p("integer")),
        ("stream_idle_timeout_seconds", p("integer")),
        ("max_concurrent_per_key", p("integer")),
        ("requests_per_second", p("integer")),
        ("burst_capacity", p("integer")),
    ],
    extra={"description": Literal(
        "Per-tenant ceilings (GW-13). Each may only lower the deployment's own "
        "figure, never raise it, and a value above it is refused. An omitted or "
        "zero field means the deployment's value.\n")},
)

SCHEMAS["TenantCache"] = obj(
    [
        ("enabled", p("boolean", "Opts every eligible request into the GW-12 cache "
                                 "without the caller sending a header.")),
        ("ttl_seconds", p("integer", "Zero means the deployment's default, not zero "
                                     "seconds. A value above `cache.max_ttl` is "
                                     "refused.")),
    ],
)

SCHEMAS["TenantDebugCapture"] = obj(
    [
        ("enabled", p("boolean")),
        ("ttl_seconds", p("integer", "Zero means the deployment's default.")),
        ("sample_rate", p("number", "Zero means the deployment's default, not "
                                    "\"capture nothing\" — `enabled` is the switch.")),
    ],
    extra={"description": Literal(
        "GW-14's one exception to the content ban. While this is on, a sampled "
        "fraction of the tenant's requests have their bodies retained, readable "
        "through the admin plane alone, until they are hard-deleted at "
        "`ttl_seconds`. There is deliberately no deployment-wide switch: turning "
        "retention on has to name whose content is being retained.\n")},
)

SCHEMAS["Tenant"] = obj(
    [
        ("id", p("string")),
        ("name", p("string")),
        ("status", p("string", None, enum=["active", "suspended"])),
        ("created_at", dt()),
        ("limits", ref("TenantLimits")),
        ("cache", ref("TenantCache")),
        ("debug_capture", ref("TenantDebugCapture")),
    ],
    required=["id", "name", "status", "created_at", "limits", "cache", "debug_capture"],
)

SCHEMAS["APIKey"] = obj(
    [
        ("id", p("string")),
        ("tenant_id", p("string", "Empty for a root admin key, which belongs to no "
                                  "tenant.")),
        ("plane", p("string", None, enum=["data", "admin"])),
        ("name", p("string")),
        ("prefix", p("string", "The display prefix, which is also what usage records "
                               "and audit entries attribute to.")),
        ("scope", p("string", "Admin keys only: `root`, or `tenant:<id>`.")),
        ("created_at", dt()),
        ("expires_at", dt()),
        ("revoked_at", dt()),
    ],
    required=["id", "tenant_id", "plane", "name", "prefix", "created_at"],
    extra={"description": Literal(
        "A key as stored. The secret is never here — only a hash is kept, so a "
        "stolen database yields no working credential.\n")},
)

SCHEMAS["MintedKey"] = obj(
    [
        ("key", ref("APIKey")),
        ("secret", p("string", "The plaintext credential. Send it as "
                               "`Authorization: Bearer <secret>`.")),
        ("warning", p("string")),
    ],
    required=["key", "secret", "warning"],
    extra={"description": Literal(
        "The show-once response from both key-creation routes. `secret` is returned "
        "exactly here and nowhere else; the stored record is nested under `key`.\n")},
)

SCHEMAS["Provider"] = obj(
    [
        ("id", p("string")),
        ("tenant_id", p("string")),
        ("name", p("string", "The routing identifier — `openai`, `anthropic`, …")),
        ("kind", p("string", "Which adapter to use. `openai` covers every "
                             "OpenAI-compatible API.")),
        ("base_url", p("string", None, format="uri")),
        ("enabled", p("boolean")),
        ("key_prefixes", arr(p("string"),
                             "Display prefixes for the registered credentials. The "
                             "credentials themselves are never returned.")),
        ("created_at", dt()),
    ],
    required=["id", "tenant_id", "name", "kind", "base_url", "enabled", "key_prefixes",
              "created_at"],
)

SCHEMAS["Alias"] = obj(
    [
        ("id", p("string")),
        ("tenant_id", p("string")),
        ("name", p("string")),
        ("pin", p("string", "A model id this alias always resolves to. A pin wins "
                            "outright; the constraint fields below are then unused.")),
        ("capabilities", arr(p("string"))),
        ("min_context_window", p("integer")),
        ("provider_preference", arr(p("string"))),
        ("cost_tier", p("string", None, enum=["cheapest", "balanced", "best"])),
        ("created_at", dt()),
    ],
    required=["id", "tenant_id", "name", "created_at"],
    extra={"description": Literal(
        "A stable name a caller may use in place of a real model id (GW-2). Without "
        "a pin the constraint fields select the best current catalog entry, so "
        "\"fast\" keeps meaning fast as providers ship new models.\n")},
)

SCHEMAS["RoutingRule"] = obj(
    [
        ("id", p("string")),
        ("tenant_id", p("string")),
        ("match", p("string", "The model or alias the caller asks for.")),
        ("chain", arr(p("string"), "Tried left to right (GW-3). A single-element chain "
                                   "is a pin with no fallback, which is a legitimate "
                                   "configuration.")),
        ("created_at", dt()),
    ],
    required=["id", "tenant_id", "match", "chain", "created_at"],
)

SCHEMAS["QuotaLimit"] = obj(
    [
        ("cap", p("number")),
        ("soft_threshold_pct", p("integer", "The percentage of `cap` at which the "
                                            "holder is warned, via "
                                            "X-CogniGate-Quota-State and a webhook "
                                            "event.")),
    ],
    required=["cap", "soft_threshold_pct"],
)

SCHEMAS["QuotaWindow"] = obj(
    [
        ("tokens", ref("QuotaLimit")),
        ("cost", ref("QuotaLimit")),
    ],
    extra={"description": "Either may be absent, so a tenant can be capped on spend "
                          "without also being capped on tokens."},
)

SCHEMAS["Quota"] = obj(
    [
        ("tenant_id", p("string")),
        ("key_id", p("string", "Present when this quota constrains one key rather than "
                               "the tenant.")),
        ("day", ref("QuotaWindow")),
        ("month", ref("QuotaWindow")),
        ("updated_at", dt()),
    ],
    required=["tenant_id", "day", "month", "updated_at"],
    extra={"description": Literal(
        "Four independent slots (GW-4): two windows in two units. An absent slot is "
        "unlimited, which is distinct from a cap of zero. A key-level quota can only "
        "narrow what the tenant's own quota already allows — both are evaluated and "
        "either may reject.\n")},
)

SCHEMAS["Webhook"] = obj(
    [
        ("id", p("string")),
        ("tenant_id", p("string")),
        ("url", p("string", None, format="uri")),
        ("events", arr(p("string"), "Subscribed event types. `GET /admin/v1/meta` "
                                    "lists everything available.")),
        ("enabled", p("boolean")),
        ("created_at", dt()),
    ],
    required=["id", "tenant_id", "url", "events", "enabled", "created_at"],
    extra={"description": Literal(
        "A delivery target. The HMAC secret is write-only: it is accepted on create "
        "and never returned. Deliveries carry it as X-CogniGate-Signature.\n")},
)

SCHEMAS["Event"] = obj(
    [
        ("id", p("string")),
        ("type", p("string")),
        ("created", dt()),
        ("tenant", p("string")),
        ("data", D(("type", "object"), ("additionalProperties", True),
                   ("description",
                    "Gateway facts — a model id, a provider name, a quota window. "
                    "Never request or response content (GW-14)."))),
    ],
    required=["id", "type", "created", "tenant", "data"],
    extra={"description": Literal(
        "One notification as it was published. The field names match the webhook "
        "envelope's exactly, so a reader can compare what it polled against what it "
        "was delivered.\n")},
)

SCHEMAS["AuditEntry"] = obj(
    [
        ("id", p("string")),
        ("at", dt()),
        ("actor", p("string", "The credential's display prefix, which is what an "
                              "operator sees when listing keys.")),
        ("actor_key_id", p("string")),
        ("actor_scope", p("string")),
        ("action", p("string", None, enum=["create", "update", "upsert", "delete"])),
        ("resource", p("string")),
        ("tenant_id", p("string")),
        ("status", p("integer", "The HTTP status the attempt produced. Refused writes "
                                "are logged too — an attempt to reach another tenant "
                                "is exactly what this log is read to find.")),
        ("request_id", p("string")),
    ],
    required=["id", "at", "actor", "actor_key_id", "actor_scope", "action", "resource",
              "status"],
    extra={"description": Literal(
        "One line of the append-only admin log (GW-6). It records who did what to "
        "which resource and how it ended, never the request body: an admin write can "
        "carry provider credentials, and a log that captured them would hand an "
        "auditor a second copy of every secret.\n")},
)

SCHEMAS["Capture"] = obj(
    [
        ("id", p("string")),
        ("request_id", p("string")),
        ("at", dt()),
        ("expires_at", dt()),
        ("model", p("string")),
        ("status", p("integer")),
        ("request", p("string", "The request body as captured, base64-encoded.",
                      format="byte")),
        ("response", p("string", "The response body as captured, base64-encoded.",
                       format="byte")),
    ],
    required=["id", "at", "expires_at", "status", "request", "response"],
    extra={"description": Literal(
        "The one place in the product where prompt content is served back out "
        "(GW-14). Read-only, admin plane, tenant-scoped, and only for a tenant whose "
        "`debug_capture` was explicitly enabled by an admin action that is itself in "
        "the audit log.\n")},
)

SCHEMAS["CatalogRefreshResult"] = obj(
    [
        ("object", p("string", None, const="catalog_refresh")),
        (
            "refreshed",
            arr(obj(
                [
                    ("tenant", p("string")),
                    ("ok", p("boolean")),
                    ("models", p("integer")),
                    ("stale", p("boolean")),
                    ("error", p("string")),
                    ("errors", arr(p("string"))),
                ],
                required=["tenant"],
            ),
                "One row per tenant. A provider that could not be reached is reported "
                "here rather than failing the whole call, so refreshing ten tenants "
                "does not hide nine results behind the first failure."),
        ),
    ],
    required=["object", "refreshed"],
)

for _name, _item, _desc in [
    ("TenantList", "Tenant", "A page of tenants."),
    ("APIKeyList", "APIKey", "A page of keys."),
    ("ProviderList", "Provider", "A page of providers."),
    ("AliasList", "Alias", "A page of aliases."),
    ("RoutingRuleList", "RoutingRule", "A page of routing rules."),
    ("WebhookList", "Webhook", "A page of webhooks."),
    ("EventList", "Event", "A page of events, newest first."),
    ("AuditList", "AuditEntry", "A page of audit entries, newest first."),
    ("CaptureList", "Capture", "A page of captures, newest first."),
]:
    SCHEMAS[_name] = listing(ref(_item), _desc)


# --- reusable components ----------------------------------------------------

HEADERS = OrderedDict([
    ("RequestId", D(
        ("description", Literal(
            "The identifier that ties this response to its structured log line, its "
            "usage record and, on a failure, the `request_id` in the error body. It "
            "is the one string a user can quote about a request.\n")),
        ("schema", p("string")),
    )),
    ("ServedBy", D(
        ("description", "The provider and model that actually served the request."),
        ("schema", p("string")),
    )),
    ("FallbackDepth", D(
        ("description", Literal(
            "How many candidates the routing cascade tried before one answered. `0` "
            "means the first choice served it.\n")),
        ("schema", p("integer")),
    )),
    ("QuotaState", D(
        ("description", Literal(
            "Where the caller stands against its GW-4 quotas after this request. "
            "`soft-exceeded` is the warning that arrives while requests are still "
            "being served.\n")),
        ("schema", p("string", None, enum=["ok", "soft-exceeded", "hard-exceeded"])),
    )),
    ("Cache", D(
        ("description", "The GW-12 disposition of this request."),
        ("schema", p("string", None, enum=["hit", "miss", "bypass"])),
    )),
    ("DebugCapture", D(
        ("description", "Present when this request's bodies were retained under the "
                        "tenant's GW-14 capture policy."),
        ("schema", p("string")),
    )),
    ("Deprecation", D(
        ("description", Literal(
            "Present when the model that served this request is marked deprecated in "
            "the catalog. GW-9 makes removing a documented header a MAJOR change, so "
            "a client may rely on this appearing.\n")),
        ("schema", p("string")),
    )),
    ("RetryAfter", D(
        ("description", "Seconds to wait before retrying."),
        ("schema", p("integer")),
    )),
])


def _err(desc):
    return D(
        ("description", desc),
        ("headers", D(("X-CogniGate-Request-Id", href("RequestId")))),
        ("content", D(("application/json", D(("schema", ref("Error")))))),
    )


def _err_retry(desc):
    r = _err(desc)
    r["headers"]["Retry-After"] = href("RetryAfter")
    r["headers"]["X-CogniGate-Quota-State"] = href("QuotaState")
    return r


RESPONSES = OrderedDict([
    ("BadRequest", _err("The request is malformed, or a parameter is outside the "
                             "range the deployment accepts.")),
    ("Unauthorized", _err("No credential, an unknown one, or one minted for the "
                               "other plane (`wrong_plane`).")),
    ("Forbidden", _err("The credential is valid but its scope does not reach this "
                            "resource.")),
    ("NotFound", _err("No such resource. A tenant-scoped key naming another "
                           "tenant also gets this, rather than a 403, so the response "
                           "does not reveal whether that tenant exists.")),
    ("Conflict", _err("The write collides with something that already exists.")),
    ("PayloadTooLarge", _err("The body is larger than `max_request_bytes` for this "
                                  "tenant.")),
    ("TooManyRequests", _err_retry("Rate limited, at the concurrency ceiling, or "
                                        "over a hard quota. `code` says which.")),
    ("BadGateway", _err("Every candidate in the routing cascade failed. On "
                             "`upstream_exhausted` the body enumerates them in "
                             "`attempts`.")),
    ("ServiceUnavailable", _err("The process is draining, or a dependency it "
                                     "needs is unreachable.")),
    ("GatewayTimeout", _err("The upstream did not answer within "
                                 "`request_timeout_seconds`, or a stream went idle for "
                                 "longer than `stream_idle_timeout_seconds`.")),
])


# --- operation helpers ------------------------------------------------------

DATA_ERRORS = ["BadRequest", "Unauthorized", "TooManyRequests", "ServiceUnavailable"]
ADMIN_ERRORS = ["BadRequest", "Unauthorized", "Forbidden", "NotFound",
                "TooManyRequests"]

STATUS_OF = {
    "BadRequest": "400", "Unauthorized": "401", "Forbidden": "403", "NotFound": "404",
    "Conflict": "409", "PayloadTooLarge": "413", "TooManyRequests": "429",
    "BadGateway": "502", "ServiceUnavailable": "503", "GatewayTimeout": "504",
}


def ok(desc, schema=None, headers=("RequestId",), status="200", media="application/json"):
    r = D(("description", desc))
    hdrs = OrderedDict()
    for h in headers:
        hdrs[{"RequestId": "X-CogniGate-Request-Id",
              "ServedBy": "X-CogniGate-Served-By",
              "FallbackDepth": "X-CogniGate-Fallback-Depth",
              "QuotaState": "X-CogniGate-Quota-State",
              "Cache": "X-CogniGate-Cache",
              "DebugCapture": "X-CogniGate-Debug-Capture",
              "Deprecation": "X-CogniGate-Deprecation"}[h]] = href(h)
    if hdrs:
        r["headers"] = hdrs
    if schema is not None:
        r["content"] = D((media, D(("schema", schema))))
    return status, r


def op(tag, summary, description, responses, errors, security, params=None, body=None,
       op_id=None):
    o = OrderedDict()
    o["tags"] = [tag]
    if op_id:
        o["operationId"] = op_id
    o["summary"] = summary
    if description:
        o["description"] = Literal(description if description.endswith("\n")
                                   else description + "\n")
    o["security"] = security
    if params:
        o["parameters"] = params
    if body is not None:
        o["requestBody"] = body
    resp = OrderedDict()
    for status, r in responses:
        resp[status] = r
    for e in errors:
        resp[STATUS_OF[e]] = rref(e)
    o["responses"] = resp
    return o


def body(schema, required=True, desc=None, example=None):
    b = OrderedDict()
    if desc:
        b["description"] = desc
    b["required"] = required
    media = D(("schema", schema))
    # An example is worth spelling out where the smallest schema-legal body is
    # not a body anyone would send. scripts/gen_postman.py prefers one of these
    # over the shape it would otherwise synthesise from the schema.
    if example is not None:
        media["example"] = example
    b["content"] = D(("application/json", media))
    return b


def path_param(name, desc):
    return D(("name", name), ("in", "path"), ("required", True),
             ("description", desc), ("schema", p("string")))


def query_param(name, desc, schema, required=False):
    q = D(("name", name), ("in", "query"))
    if required:
        q["required"] = True
    q["description"] = desc
    q["schema"] = schema
    return q


TENANT_PARAM = path_param(
    "tenant",
    "Tenant id. A root key reaches any tenant; a `tenant:<id>` key reaches only its "
    "own, and naming another is a 404.")

PAGE_PARAMS = [
    query_param("limit", "Page size, 1 to 200. A value outside the range is refused "
                         "rather than clamped, because a clamped short page is "
                         "indistinguishable from a genuine last page.",
                p("integer", None, minimum=1, maximum=200, default=50)),
    query_param("after", "Cursor: the `id` of the last item the caller saw. An unknown "
                         "cursor is refused rather than restarting from the beginning.",
                p("string")),
]

WINDOW_PARAM = query_param("window", "The period to report over.",
                           p("string", None, enum=["day", "month"], default="day"))

GROUP_BY_PARAM = query_param("group_by", "The dimension to aggregate on.",
                             p("string", None,
                               enum=["model", "provider", "key", "client_request_id"],
                               default="model"))

DATA = [{"DataKey": []}]
ADMIN = [{"AdminKey": []}]
NONE = []


# --- paths ------------------------------------------------------------------

PATHS = OrderedDict()


def add(path, method, operation):
    PATHS.setdefault(path, OrderedDict())[method] = operation


# Operational, unauthenticated.

add("/healthz", "get", op(
    "Operations",
    "Liveness probe.",
    "Deliberately unauthenticated and deliberately thin: anything that can reach the "
    "socket can read it, so it carries no build version. `/v1/health` and `/v1/meta` "
    "report the version, and both require a credential.",
    [ok("Serving.", obj([("status", p("string", None, const="ok"))],
                        required=["status"]), headers=()),
     ok("Draining. The process is refusing new work while it finishes what it has; "
        "a load balancer should stop sending here.",
        obj([("status", p("string", None, const="draining"))], required=["status"]),
        headers=(), status="503")],
    [], NONE, op_id="healthz"))

add("/metrics", "get", op(
    "Operations",
    "Prometheus scrape.",
    "The GW-8 exposition for the gateway process. Unauthenticated by default, because "
    "the thing that scrapes it is a sidecar with no notion of tenant credentials; "
    "setting `metrics.token` puts it behind a bearer token. The path is configurable "
    "and the endpoint can be turned off entirely, so a deployment may not serve this "
    "at all — `GET /v1/meta` reports whether it does.\n\n"
    "The analytics engine publishes its own scrape at `GET /metrics` on port 8081. "
    "That is a separate process and is not described by this document.",
    [ok("The exposition, in the Prometheus text format. No series carries request or "
        "response content.", p("string"), headers=(),
        media="text/plain; version=0.0.4")],
    ["Unauthorized"], NONE, op_id="metrics"))


# Data plane.

add("/v1/chat/completions", "post", op(
    "Chat",
    "Create a chat completion.",
    "The OpenAI-compatible entry point. `model` may be a real model id, an alias "
    "(GW-2) or the match side of a routing rule (GW-3); what actually served the "
    "request is reported in the response body and in X-CogniGate-Served-By.\n\n"
    "With `\"stream\": true` the response is `text/event-stream`. A stream that "
    "stalls terminates with an SSE error event carrying code `upstream_stream_stalled` "
    "— the HTTP status line was already sent, so it cannot be a status.",
    [ok("A completion.", ref("ChatResponse"),
        headers=("RequestId", "ServedBy", "FallbackDepth", "QuotaState", "Cache",
                 "DebugCapture", "Deprecation")),
     ("200-stream", None)],  # replaced below
    ["BadRequest", "Unauthorized", "NotFound", "PayloadTooLarge", "TooManyRequests",
     "BadGateway", "ServiceUnavailable", "GatewayTimeout"],
    DATA,
    # `content` is optional on a message because an assistant tool call has
    # none, so the smallest legal body is a role with nothing in it. That is
    # not a request anyone sends; spell out one that is.
    body=body(ref("ChatRequest"), example=D(
        ("model", "gpt-4o-mini"),
        ("messages", [D(("role", "user"), ("content", "Hello."))]))),
    op_id="createChatCompletion"))

# The 200 carries two media types; build it explicitly rather than through ok().
_chat_200 = PATHS["/v1/chat/completions"]["post"]["responses"]["200"]
_chat_200["content"]["text/event-stream"] = D(
    ("schema", p("string", "Server-sent events. Each `data:` line is an OpenAI "
                           "`chat.completion.chunk`, and the stream ends with "
                           "`data: [DONE]`.")))
del PATHS["/v1/chat/completions"]["post"]["responses"]["200-stream"]

add("/v1/models", "get", op(
    "Models",
    "List available models.",
    "Every model the tenant's providers advertise (GW-1), plus its aliases (GW-2), in "
    "OpenAI's list shape. Aliases are marked `cognigate.alias` and carry what they "
    "currently resolve to.",
    [ok("The catalog.", ref("ModelList"))],
    ["Unauthorized", "TooManyRequests", "ServiceUnavailable"], DATA, op_id="listModels"))

add("/v1/models/{model}", "get", op(
    "Models",
    "Retrieve one model or alias.",
    "Send the id raw. A model id routinely contains a slash — `meta-llama/Llama-3-70b` "
    "is an ordinary id at several providers — and the route captures the whole "
    "remainder of the path, so the slashes need no escaping. An OpenAPI path parameter "
    "cannot express that, which is the one place a generated client needs a hand: the "
    "gateway does not percent-decode this segment, so a client that encodes the slash "
    "as `%2F` will look up a model whose id contains those three characters and get a "
    "404.",
    [ok("The entry.", ref("ModelObject"))],
    ["Unauthorized", "NotFound", "TooManyRequests", "ServiceUnavailable"], DATA,
    params=[path_param("model", "A model id or an alias name.")],
    op_id="getModel"))

add("/v1/usage", "get", op(
    "Usage",
    "Usage and quota state for the calling tenant.",
    "The same window the quota rejections are computed over, so a caller asking why it "
    "was refused sees the figures the refusal used.",
    [ok("Totals and every quota slot that applies.", ref("UsageResponse"))],
    DATA_ERRORS, DATA, params=[WINDOW_PARAM], op_id="getUsage"))

add("/v1/usage/breakdown", "get", op(
    "Usage",
    "Usage broken down by one dimension.",
    "Rows are ordered by spend. At most 200 are returned; when there were more, "
    "`truncated` is true and the cheapest were dropped.",
    [ok("The breakdown.", ref("UsageBreakdown"))],
    DATA_ERRORS, DATA, params=[WINDOW_PARAM, GROUP_BY_PARAM],
    op_id="getUsageBreakdown"))

add("/v1/health", "get", op(
    "Operations",
    "Dependency and configuration health for the calling tenant.",
    "Answers 200 whatever the status field says: this reports on the deployment, and a "
    "non-2xx would make the report itself unreadable to the client that needs it. "
    "Outside the rate limit, so a client polling it during an incident is not the "
    "thing that exhausts its own budget.",
    [ok("The report.", ref("HealthReport"))],
    ["Unauthorized", "ServiceUnavailable"], DATA, op_id="getHealth"))

add("/v1/meta", "get", op(
    "Operations",
    "What this deployment is and what it supports.",
    "GW-9 makes this the supported way to feature-detect: a client reads `features` "
    "and `capabilities` rather than probing endpoints to see what answers. Outside the "
    "rate limit, for the same reason `/v1/health` is.",
    [ok("The descriptor.", ref("Meta"))],
    ["Unauthorized", "ServiceUnavailable"], DATA, op_id="getMeta"))


# Admin plane.

add("/admin/v1/meta", "get", op(
    "Admin",
    "What the calling admin key can do.",
    "Reports the credential's own scope and the full event vocabulary a webhook may "
    "subscribe to, so an operator does not have to discover event types by triggering "
    "them.",
    [ok("The descriptor.", ref("AdminMeta"))],
    ["Unauthorized", "TooManyRequests"], ADMIN, op_id="getAdminMeta"))

add("/admin/v1/catalog/refresh", "post", op(
    "Admin",
    "Re-discover provider catalogs now.",
    "Without this a model added or retired at a provider only becomes visible when the "
    "TTL expires. A root key with no body refreshes every tenant; naming one narrows "
    "it. A tenant-scoped key is narrowed to its own, and naming another is a 404.",
    [ok("One result row per tenant refreshed.", ref("CatalogRefreshResult"))],
    ["BadRequest", "Unauthorized", "NotFound", "TooManyRequests"], ADMIN,
    body=body(obj([("tenant", p("string", "Refresh only this tenant."))]),
              required=False),
    op_id="refreshCatalog"))

add("/admin/v1/audit", "get", op(
    "Admin",
    "Read the admin audit log.",
    "Append-only, newest first, and it includes refused writes: an attempt to reach "
    "another tenant is exactly what this log is read to find, and one that succeeded "
    "is not more interesting than one that did not.",
    [ok("A page of entries.", ref("AuditList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "TooManyRequests"], ADMIN,
    params=list(PAGE_PARAMS), op_id="listAudit"))

add("/admin/v1/admin-keys", "post", op(
    "Admin keys",
    "Mint a root admin key.",
    "How a deployment rotates away from its bootstrap credential. Root keys belong to "
    "no tenant, so they are not under `/tenants` — minting one there would tie the "
    "credential that outranks every tenant to the lifetime of one of them. Requires a "
    "root key.",
    [ok("Created. The secret is shown here and nowhere else.", ref("MintedKey"),
        status="201")],
    ["BadRequest", "Unauthorized", "Forbidden", "TooManyRequests"], ADMIN,
    body=body(obj([("name", p("string")),
                   ("expires_at", dt("Must be in the future. Omit for a key that does "
                                     "not expire."))],
                  required=["name"])),
    op_id="createAdminKey"))

add("/admin/v1/admin-keys", "get", op(
    "Admin keys",
    "List root admin keys.",
    "Requires a root key.",
    [ok("A page of keys.", ref("APIKeyList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "TooManyRequests"], ADMIN,
    params=list(PAGE_PARAMS), op_id="listAdminKeys"))

add("/admin/v1/admin-keys/{id}", "delete", op(
    "Admin keys",
    "Revoke a root admin key.",
    "There is deliberately no guard against revoking the last one: the bootstrap key "
    "is checked against the process environment rather than resolved through the "
    "store, so it survives any revocation here and remains the documented way back in.",
    [ok("Revoked.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[path_param("id", "Key id.")], op_id="revokeAdminKey"))

add("/admin/v1/tenants", "post", op(
    "Tenants",
    "Create a tenant.",
    "Requires a root key.",
    [ok("Created.", ref("Tenant"), status="201")],
    ["BadRequest", "Unauthorized", "Forbidden", "Conflict", "TooManyRequests"], ADMIN,
    body=body(obj([("name", p("string"))], required=["name"])),
    op_id="createTenant"))

add("/admin/v1/tenants", "get", op(
    "Tenants",
    "List tenants.",
    "Requires a root key. A tenant-scoped key is refused rather than shown a "
    "one-row list: the tenant it belongs to is already named in every path it "
    "can reach, so the listing exists only for the operator view.",
    [ok("A page of tenants.", ref("TenantList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "TooManyRequests"], ADMIN,
    params=list(PAGE_PARAMS), op_id="listTenants"))

add("/admin/v1/tenants/{tenant}", "get", op(
    "Tenants",
    "Retrieve a tenant.",
    "",
    [ok("The tenant. `warnings` carries any configuration the deployment accepted but "
        "would not have chosen — an override at the deployment ceiling, for instance.",
        obj([("id", p("string")), ("name", p("string")),
             ("status", p("string", None, enum=["active", "suspended"])),
             ("created_at", dt()),
             ("limits", ref("TenantLimits")), ("cache", ref("TenantCache")),
             ("debug_capture", ref("TenantDebugCapture")),
             ("warnings", arr(p("string")))],
            required=["id", "name", "status", "created_at"]))],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM], op_id="getTenant"))

add("/admin/v1/tenants/{tenant}", "patch", op(
    "Tenants",
    "Update a tenant.",
    "Requires a root key: a tenant may not lift its own limits or un-suspend "
    "itself. Absent and null are distinct: a body carrying only `status` leaves the name "
    "alone. `limits`, `cache` and `debug_capture` each replace their whole block "
    "rather than merging field by field, so an empty object clears every override — "
    "\"send me the policy you want this tenant to have\" is the only rule to remember.",
    [ok("The updated tenant.", ref("Tenant"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM],
    body=body(obj([("name", p("string")),
                   ("status", p("string", None, enum=["active", "suspended"])),
                   ("limits", ref("TenantLimits")),
                   ("cache", ref("TenantCache")),
                   ("debug_capture", ref("TenantDebugCapture"))])),
    op_id="updateTenant"))

add("/admin/v1/tenants/{tenant}", "delete", op(
    "Tenants",
    "Delete a tenant and everything under it.",
    "Requires a root key and `?confirm=` matching the tenant id in the path — the "
    "whole id rather than a bare `?confirm=true`, which a mis-pasted URL would "
    "satisfy. Cached responses and retained captures go with it: both are reachable "
    "only through credentials that have just ceased to exist, so leaving them would be "
    "retention with no reader.",
    [ok("Deleted.", None, headers=("RequestId",), status="204")],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM,
            query_param("confirm", "Must equal the tenant id in the path.", p("string"),
                        required=True)],
    op_id="deleteTenant"))

add("/admin/v1/tenants/{tenant}/keys", "post", op(
    "Tenant keys",
    "Mint a key for a tenant.",
    "`plane` decides what the key can reach: a `data` key (`cg-…`) reaches `/v1/*` and "
    "nothing else, an `admin` key (`cga-…`) reaches `/admin/v1/*`. That separation is "
    "what lets an application key be handed to a service without also handing over the "
    "ability to mint more keys. A tenant-scoped caller cannot mint a key whose scope "
    "exceeds its own.",
    [ok("Created. The secret is shown here and nowhere else.", ref("MintedKey"),
        status="201")],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM],
    body=body(obj([("name", p("string")),
                   ("plane", p("string", None, enum=["data", "admin"], default="data")),
                   ("scope", p("string", "Admin keys only.")),
                   ("expires_at", dt("Must be in the future."))],
                  required=["name"])),
    op_id="createTenantKey"))

add("/admin/v1/tenants/{tenant}/keys", "get", op(
    "Tenant keys",
    "List a tenant's keys.",
    "",
    [ok("A page of keys. No secret is ever returned.", ref("APIKeyList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM] + list(PAGE_PARAMS), op_id="listTenantKeys"))

add("/admin/v1/tenants/{tenant}/keys/{id}", "delete", op(
    "Tenant keys",
    "Revoke a key.",
    "",
    [ok("Revoked.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Key id.")], op_id="revokeTenantKey"))

add("/admin/v1/tenants/{tenant}/keys/{id}/quota", "put", op(
    "Quotas",
    "Set a per-key quota.",
    "A key quota can only narrow what the tenant's own quota already allows: both are "
    "evaluated and a request is rejected if either says so, so this can never raise a "
    "key past its tenant's ceiling.",
    [ok("The stored quota.", ref("Quota"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Key id.")],
    body=body(obj([("day", ref("QuotaWindow")), ("month", ref("QuotaWindow"))])),
    op_id="setKeyQuota"))

add("/admin/v1/tenants/{tenant}/keys/{id}/quota", "get", op(
    "Quotas",
    "Retrieve a per-key quota.",
    "",
    [ok("The quota.", ref("Quota"))],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Key id.")], op_id="getKeyQuota"))

add("/admin/v1/tenants/{tenant}/keys/{id}/quota", "delete", op(
    "Quotas",
    "Remove a per-key quota.",
    "The key falls back to its tenant's quota alone.",
    [ok("Removed.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Key id.")], op_id="deleteKeyQuota"))

add("/admin/v1/tenants/{tenant}/providers", "post", op(
    "Providers",
    "Register an upstream provider.",
    "`keys` is a pool rather than a single credential: GW-3 rotates within it on a 429 "
    "before it gives up on the provider and cascades onward. The credentials are "
    "write-only — listing a provider returns only their display prefixes.",
    [ok("Created.", ref("Provider"), status="201")],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "Conflict",
     "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM],
    body=body(obj([("name", p("string", "The routing identifier.")),
                   ("kind", p("string", "Which adapter to use.")),
                   ("base_url", p("string", None, format="uri")),
                   ("keys", arr(p("string"), "At least one. Never returned again.")),
                   ("enabled", p("boolean", None, default=True))],
                  required=["name", "kind", "base_url", "keys"])),
    op_id="createProvider"))

add("/admin/v1/tenants/{tenant}/providers", "get", op(
    "Providers",
    "List a tenant's providers.",
    "",
    [ok("A page of providers.", ref("ProviderList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM] + list(PAGE_PARAMS), op_id="listProviders"))

add("/admin/v1/tenants/{tenant}/providers/{id}", "patch", op(
    "Providers",
    "Update a provider.",
    "An empty `keys` array is refused: a provider with no credentials could never "
    "serve a request, so accepting the write would produce a failure at dispatch time "
    "instead of at the moment the mistake was made.",
    [ok("The updated provider.", ref("Provider"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Provider id.")],
    body=body(obj([("base_url", p("string", None, format="uri")),
                   ("enabled", p("boolean")),
                   ("keys", arr(p("string"), "Replaces the whole pool."))])),
    op_id="updateProvider"))

add("/admin/v1/tenants/{tenant}/providers/{id}", "delete", op(
    "Providers",
    "Remove a provider.",
    "",
    [ok("Removed.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Provider id.")], op_id="deleteProvider"))

add("/admin/v1/tenants/{tenant}/aliases/{name}", "put", op(
    "Aliases",
    "Create or replace an alias.",
    "Idempotent by name. A `pin` wins outright; otherwise the constraint fields select "
    "the best current catalog entry each time the alias is resolved, which is what "
    "lets \"fast\" keep meaning fast as providers ship new models. An alias whose name "
    "collides with a real model id is refused.",
    [ok("The stored alias.", ref("Alias"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "Conflict",
     "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("name", "The alias name callers will send as "
                                             "`model`.")],
    body=body(obj([("pin", p("string", "A model id this alias always resolves to.")),
                   ("capabilities", arr(p("string"))),
                   ("min_context_window", p("integer")),
                   ("provider_preference", arr(p("string"))),
                   ("cost_tier", p("string", None,
                                   enum=["cheapest", "balanced", "best"]))])),
    op_id="putAlias"))

add("/admin/v1/tenants/{tenant}/aliases", "get", op(
    "Aliases",
    "List a tenant's aliases.",
    "",
    [ok("A page of aliases.", ref("AliasList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM] + list(PAGE_PARAMS), op_id="listAliases"))

add("/admin/v1/tenants/{tenant}/aliases/{name}", "delete", op(
    "Aliases",
    "Delete an alias.",
    "",
    [ok("Deleted.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("name", "The alias name.")],
    op_id="deleteAlias"))

add("/admin/v1/tenants/{tenant}/routing-rules", "put", op(
    "Routing",
    "Create or replace a routing rule.",
    "Idempotent by `match`. The chain is tried left to right until one answers; a "
    "single-element chain is a pin with no fallback, which is a legitimate "
    "configuration. A duplicate entry in the chain is refused, and a chain longer than "
    "`max_fallback_depth` is refused rather than silently truncated.",
    [ok("The stored rule.", ref("RoutingRule"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM],
    body=body(obj([("match", p("string")),
                   ("chain", arr(p("string")))],
                  required=["match", "chain"])),
    op_id="putRoutingRule"))

add("/admin/v1/tenants/{tenant}/routing-rules", "get", op(
    "Routing",
    "List a tenant's routing rules.",
    "",
    [ok("A page of rules.", ref("RoutingRuleList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM] + list(PAGE_PARAMS), op_id="listRoutingRules"))

add("/admin/v1/tenants/{tenant}/routing-rules/{id}", "delete", op(
    "Routing",
    "Delete a routing rule.",
    "",
    [ok("Deleted.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Rule id.")], op_id="deleteRoutingRule"))

add("/admin/v1/tenants/{tenant}/quota", "put", op(
    "Quotas",
    "Set a tenant quota.",
    "The four slots are independent and any may be omitted, which means unlimited for "
    "that slot — distinct from a cap of zero, which is a meaningful configuration.",
    [ok("The stored quota.", ref("Quota"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM],
    body=body(obj([("day", ref("QuotaWindow")), ("month", ref("QuotaWindow"))])),
    op_id="setTenantQuota"))

add("/admin/v1/tenants/{tenant}/quota", "get", op(
    "Quotas",
    "Retrieve a tenant quota.",
    "",
    [ok("The quota.", ref("Quota"))],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM], op_id="getTenantQuota"))

add("/admin/v1/tenants/{tenant}/quota", "delete", op(
    "Quotas",
    "Remove a tenant quota.",
    "The tenant becomes uncapped, subject to any per-key quotas that remain.",
    [ok("Removed.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM], op_id="deleteTenantQuota"))

add("/admin/v1/tenants/{tenant}/cache/flush", "post", op(
    "Cache",
    "Drop a tenant's cached responses.",
    "",
    [ok("How many entries were dropped.",
        obj([("flushed", p("integer"))], required=["flushed"]))],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM], op_id="flushTenantCache"))

add("/admin/v1/tenants/{tenant}/captures", "get", op(
    "Captures",
    "Read retained request and response bodies.",
    "The one route in the product that serves prompt content back out (GW-14). It "
    "returns something only for a tenant whose `debug_capture` an admin explicitly "
    "enabled, and that enabling is itself in the audit log. Entries are hard-deleted "
    "at their `expires_at`.",
    [ok("A page of captures.", ref("CaptureList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM] + list(PAGE_PARAMS), op_id="listCaptures"))

add("/admin/v1/tenants/{tenant}/webhooks", "post", op(
    "Webhooks",
    "Register a delivery target.",
    "`secret` is write-only and is never returned. Deliveries are signed with it in "
    "X-CogniGate-Signature, so a receiver can tell a genuine delivery from anything "
    "else that finds the URL.",
    [ok("Created.", ref("Webhook"), status="201")],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM],
    body=body(obj([("url", p("string", None, format="uri")),
                   ("secret", p("string", "HMAC key. Never returned.")),
                   ("events", arr(p("string"),
                                  "Event types to subscribe to; `GET /admin/v1/meta` "
                                  "lists them all.")),
                   ("enabled", p("boolean", None, default=True))],
                  required=["url", "events"])),
    op_id="createWebhook"))

add("/admin/v1/tenants/{tenant}/webhooks", "get", op(
    "Webhooks",
    "List a tenant's webhooks.",
    "",
    [ok("A page of webhooks.", ref("WebhookList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM] + list(PAGE_PARAMS), op_id="listWebhooks"))

add("/admin/v1/tenants/{tenant}/webhooks/{id}", "delete", op(
    "Webhooks",
    "Remove a delivery target.",
    "",
    [ok("Removed.", None, headers=("RequestId",), status="204")],
    ["Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, path_param("id", "Webhook id.")], op_id="deleteWebhook"))

add("/admin/v1/tenants/{tenant}/events", "get", op(
    "Webhooks",
    "Poll a tenant's event history.",
    "Independent of delivery, which is the point: a tenant with no webhook registered, "
    "or one whose endpoint was down for the five attempts a delivery gets, still has "
    "to be able to find out that its breaker opened. Polling is the floor under "
    "at-least-once delivery, not an alternative to it — the field names match the "
    "webhook envelope's exactly so the two can be compared.",
    [ok("A page of events, newest first.", ref("EventList"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM] + list(PAGE_PARAMS), op_id="listEvents"))

add("/admin/v1/tenants/{tenant}/usage", "get", op(
    "Usage",
    "Usage and quota state for one tenant.",
    "The admin-plane view of `GET /v1/usage`, for an operator who holds no data key "
    "for the tenant.",
    [ok("Totals and every quota slot that applies.", ref("UsageResponse"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, WINDOW_PARAM], op_id="getTenantUsage"))

add("/admin/v1/tenants/{tenant}/usage/breakdown", "get", op(
    "Usage",
    "Usage for one tenant, broken down by one dimension.",
    "",
    [ok("The breakdown.", ref("UsageBreakdown"))],
    ["BadRequest", "Unauthorized", "Forbidden", "NotFound", "TooManyRequests"], ADMIN,
    params=[TENANT_PARAM, WINDOW_PARAM, GROUP_BY_PARAM],
    op_id="getTenantUsageBreakdown"))


# --- document ---------------------------------------------------------------

DOC = OrderedDict()
DOC["openapi"] = "3.1.0"
DOC["info"] = D(
    ("title", "CogniGate API"),
    ("version", "1.0.0"),
    ("summary", "Self-hosted OpenAI-compatible gateway: routing, quotas, caching and "
                "metering across providers."),
    ("description", Literal(
        "One process serves both planes on one port.\n\n"
        "- **The data plane**, under `/v1`, is OpenAI-compatible. An existing SDK "
        "pointed at this base URL works unchanged, errors included.\n"
        "- **The admin plane**, under `/admin/v1`, configures tenants, keys, "
        "providers, aliases, routes, quotas and webhooks.\n\n"
        "A key is minted for exactly one plane and is never valid on the other; "
        "presenting a data key to `/admin/v1` fails with `wrong_plane` rather than "
        "with a 404.\n\n"
        "This document describes every route the gateway serves. A reconciliation "
        "test compares it against the running route table in both directions, so a "
        "route added without an entry here — or an entry here for a route that does "
        "not exist — fails the build.\n")),
    ("license", D(("name", "Apache-2.0"),
                  ("url", "https://www.apache.org/licenses/LICENSE-2.0"))),
)
DOC["servers"] = [
    D(("url", "http://localhost:8080"),
      ("description", "The reference deployment. Both planes are on this one origin.")),
]
DOC["tags"] = [
    D(("name", "Chat"), ("description", "The OpenAI-compatible completion endpoint.")),
    D(("name", "Models"), ("description", "Catalog discovery and alias resolution.")),
    D(("name", "Usage"), ("description", "Metering and quota state.")),
    D(("name", "Operations"), ("description", "Health, capability discovery and the "
                                              "Prometheus scrape.")),
    D(("name", "Admin"), ("description", "Deployment-wide control-plane operations.")),
    D(("name", "Tenants"), ("description", "The unit of isolation.")),
    D(("name", "Tenant keys"), ("description", "Credentials scoped to one tenant.")),
    D(("name", "Admin keys"), ("description", "Root credentials, which belong to no "
                                              "tenant.")),
    D(("name", "Providers"), ("description", "Upstream accounts.")),
    D(("name", "Aliases"), ("description", "Stable names for models that change.")),
    D(("name", "Routing"), ("description", "Fallback chains.")),
    D(("name", "Quotas"), ("description", "Token and spend ceilings.")),
    D(("name", "Cache"), ("description", "The response cache.")),
    D(("name", "Captures"), ("description", "Opt-in body retention for debugging.")),
    D(("name", "Webhooks"), ("description", "Event delivery and the polling floor "
                                            "under it.")),
]
DOC["paths"] = PATHS
DOC["components"] = D(
    ("securitySchemes", D(
        ("DataKey", D(
            ("type", "http"),
            ("scheme", "bearer"),
            ("description", Literal(
                "A data-plane key, prefixed `cg-`. Valid on `/v1/*` only. The prefix "
                "is part of the credential rather than decoration: it is how the "
                "gateway answers \"wrong plane\" without a store lookup, and how "
                "secret scanners recognise a leaked CogniGate key in a public "
                "repository.\n")),
        )),
        ("AdminKey", D(
            ("type", "http"),
            ("scheme", "bearer"),
            ("description", Literal(
                "An admin-plane key, prefixed `cga-`, or the deployment's bootstrap "
                "key. Valid on `/admin/v1/*` only. Scope is either `root`, which "
                "reaches every tenant, or `tenant:<id>`, which reaches one.\n")),
        )),
    )),
    ("headers", HEADERS),
    ("responses", RESPONSES),
    ("schemas", SCHEMAS),
)


def main():
    out = yaml.dump(DOC, Dumper=Dumper, sort_keys=False, allow_unicode=True,
                    width=88, default_flow_style=False)
    header = NL.join([
        "# Generated by scripts/gen_openapi.py. Regenerate rather than hand-editing:",
        "#",
        "#     python scripts/gen_openapi.py",
        "#",
        "# The path set is reconciled against the gateway's own route table, in",
        "# both directions, by gateway/internal/server/openapi_test.go.",
        "",
    ])
    dest = sys.argv[1] if len(sys.argv) > 1 else "openapi.yaml"
    with io.open(dest, "w", encoding="utf-8", newline=NL) as fh:
        fh.write(header)
        fh.write(out)


if __name__ == "__main__":
    main()
