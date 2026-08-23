"""Generate postman_collection.json from openapi.yaml.

Run from the repository root, after regenerating the specification:

    python scripts/gen_openapi.py
    python scripts/gen_postman.py

The collection is derived rather than written, so it cannot describe an endpoint
the gateway does not serve — openapi.yaml is already reconciled against the
router by gateway/internal/server/openapi_test.go, and this file inherits that
guarantee. The one thing carried over by hand is the collection id, so that
importing a new version updates the existing collection in a workspace instead
of appearing beside it.
"""

import io
import json
import sys

import yaml

NL = chr(10)

# The id Postman uses to recognise this as the same collection across imports.
COLLECTION_ID = "8bb3805f-0b33-4b68-961f-d748f3cf4456"
SCHEMA = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"

# Which collection variable holds the credential for each security scheme.
KEY_VARIABLE = {"DataKey": "dataKey", "AdminKey": "adminKey"}

# A field whose name matches one of these gets an obviously-empty placeholder
# rather than a plausible-looking value. Nothing in this file should ever be
# mistakable for a real credential.
SECRET_HINTS = ("key", "secret", "token", "password", "credential")


def load(path):
    with io.open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


class Examples(object):
    """Builds a minimal example body from a schema, resolving refs as it goes."""

    def __init__(self, doc):
        self.schemas = doc.get("components", {}).get("schemas", {})

    def resolve(self, schema):
        seen = set()
        while isinstance(schema, dict) and "$ref" in schema:
            name = schema["$ref"].rsplit("/", 1)[-1]
            if name in seen:
                return {}
            seen.add(name)
            schema = self.schemas.get(name, {})
        return schema or {}

    def value(self, schema, field="", depth=0):
        schema = self.resolve(schema)
        if depth > 6:
            return None

        if "const" in schema:
            return schema["const"]
        if schema.get("enum"):
            return schema["enum"][0]

        types = schema.get("type")
        if isinstance(types, list):
            types = next((t for t in types if t != "null"), types[0])

        if types == "object" or "properties" in schema:
            props = schema.get("properties", {})
            required = schema.get("required", [])
            # Required fields alone make the smallest body that will be
            # accepted. A body with no required fields is a partial update, and
            # there the useful thing to show is every field that may be sent.
            wanted = required or list(props)
            out = {}
            for name in props:
                if name in wanted:
                    out[name] = self.value(props[name], name, depth + 1)
            return out
        if types == "array":
            return [self.value(schema.get("items", {}), field, depth + 1)]
        if types == "boolean":
            return False
        if types in ("integer", "number"):
            for key in ("default", "minimum"):
                if key in schema:
                    return schema[key]
            return 1 if types == "integer" else 0.0
        return self.string(schema, field)

    def string(self, schema, field):
        lowered = field.lower()
        if any(hint in lowered for hint in SECRET_HINTS):
            return "<" + (field.replace("_", " ") or "value") + ">"
        if schema.get("format") == "date-time":
            return "2026-01-01T00:00:00Z"
        return "<" + (field.replace("_", " ") or "value") + ">"


def url_for(path, operation, examples):
    """Postman wants the URL both raw and split into its parts."""
    raw_path = path.replace("{", ":").replace("}", "")
    segments = [s for s in raw_path.split("/") if s]

    params = operation.get("parameters", [])
    query = []
    for prm in params:
        if prm.get("in") != "query":
            continue
        entry = {
            "key": prm["name"],
            "value": str(examples.value(prm.get("schema", {}), prm["name"])),
            # An optional parameter ships switched off, so the request works as
            # imported and every knob is still visible in the Postman UI.
            "disabled": not prm.get("required", False),
        }
        if prm.get("description"):
            entry["description"] = prm["description"]
        query.append(entry)

    variables = []
    for prm in params:
        if prm.get("in") != "path":
            continue
        variables.append({
            "key": prm["name"],
            "value": "<" + prm["name"] + ">",
            "description": prm.get("description", ""),
        })

    raw = "{{baseUrl}}" + raw_path
    if query:
        enabled = [q for q in query if not q["disabled"]]
        if enabled:
            raw += "?" + "&".join(q["key"] + "=" + q["value"] for q in enabled)

    url = {"raw": raw, "host": ["{{baseUrl}}"], "path": segments}
    if query:
        url["query"] = query
    if variables:
        url["variable"] = variables
    return url


def auth_for(operation):
    security = operation.get("security")
    if not security:
        return {"type": "noauth"}
    scheme = list(security[0])[0]
    variable = KEY_VARIABLE.get(scheme)
    if variable is None:
        return {"type": "noauth"}
    return {
        "type": "bearer",
        "bearer": [{"key": "token", "value": "{{" + variable + "}}", "type": "string"}],
    }


def request_for(path, method, operation, examples):
    headers = []
    body = None
    content = operation.get("requestBody", {}).get("content", {})
    if "application/json" in content:
        media = content["application/json"]
        # A declared example beats a synthesised one: it is there precisely
        # because the smallest schema-legal body was not a useful one.
        if "example" in media:
            example = media["example"]
        else:
            example = examples.value(media.get("schema", {}))
        headers.append({"key": "Content-Type", "value": "application/json"})
        body = {
            "mode": "raw",
            "raw": json.dumps(example, indent=2, ensure_ascii=False),
            "options": {"raw": {"language": "json"}},
        }

    request = {
        "method": method.upper(),
        "header": headers,
        "url": url_for(path, operation, examples),
        "auth": auth_for(operation),
    }
    if operation.get("description"):
        request["description"] = operation["description"]
    if body is not None:
        request["body"] = body
    return {"name": operation.get("summary", method.upper() + " " + path),
            "request": request}


def build(doc):
    examples = Examples(doc)

    folders = []
    index = {}
    for tag in doc.get("tags", []):
        folder = {"name": tag["name"], "item": []}
        if tag.get("description"):
            folder["description"] = tag["description"]
        index[tag["name"]] = folder
        folders.append(folder)

    for path, item in doc["paths"].items():
        for method, operation in item.items():
            tag = (operation.get("tags") or ["Other"])[0]
            if tag not in index:
                folder = {"name": tag, "item": []}
                index[tag] = folder
                folders.append(folder)
            index[tag]["item"].append(
                request_for(path, method, operation, examples))

    servers = doc.get("servers") or [{"url": "http://localhost:8080"}]
    info = doc.get("info", {})

    return {
        "info": {
            "_postman_id": COLLECTION_ID,
            "name": info.get("title", "CogniGate API"),
            "description": info.get("summary", ""),
            "schema": SCHEMA,
        },
        "item": [f for f in folders if f["item"]],
        "variable": [
            {"key": "baseUrl", "value": servers[0]["url"], "type": "string"},
            {"key": "dataKey", "value": "", "type": "string",
             "description": "A data-plane key (cg-...). Reaches /v1 only."},
            {"key": "adminKey", "value": "", "type": "string",
             "description": "An admin-plane key (cga-...), or the bootstrap key. "
                            "Reaches /admin/v1 only."},
        ],
    }


def main():
    source = sys.argv[1] if len(sys.argv) > 1 else "openapi.yaml"
    dest = sys.argv[2] if len(sys.argv) > 2 else "postman_collection.json"
    collection = build(load(source))
    with io.open(dest, "w", encoding="utf-8", newline=NL) as fh:
        json.dump(collection, fh, indent=2, ensure_ascii=False)
        fh.write(NL)


if __name__ == "__main__":
    main()
