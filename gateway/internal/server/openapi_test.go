package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// openapi.yaml is a promise about what this deployment serves, and a promise
// nothing checks is a paraphrase. These tests reconcile the document against
// the router itself, in both directions: a route added without an entry fails,
// and an entry describing a route that no longer exists fails too. The second
// direction is the one that matters over time — a route is easy to notice going
// in, a stale entry is not.

// starRoutes maps a Fiber wildcard onto the path template the document uses.
// An OpenAPI path parameter cannot contain a slash, and a model id routinely
// does (`meta-llama/Llama-3-70b`), so the wildcard is the honest router
// spelling and `{model}` is the closest the specification can express. Every
// such pair is listed here rather than inferred, so adding a wildcard route
// forces a deliberate decision about how to document it.
var starRoutes = map[string]string{
	"/v1/models/*": "/v1/models/{model}",
}

type openapiDoc struct {
	Paths map[string]map[string]struct {
		OperationID string `yaml:"operationId"`
		Summary     string `yaml:"summary"`
	} `yaml:"paths"`
}

func loadOpenAPI(t *testing.T) openapiDoc {
	t.Helper()
	path := filepath.Join("..", "..", "..", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc openapiDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatal("openapi.yaml declares no paths")
	}
	return doc
}

// routeOperations returns every operation the router actually serves, keyed as
// "get /v1/models" to match the document's own spelling.
func routeOperations(t *testing.T) map[string]bool {
	t.Helper()
	h := newHarness(t)
	ops := make(map[string]bool)
	for _, r := range h.srv.App().GetRoutes(true) {
		// Fiber mirrors a HEAD onto every GET. That is transport behaviour,
		// not a documented operation.
		if r.Method == "HEAD" {
			continue
		}
		path := r.Path
		if mapped, ok := starRoutes[path]; ok {
			path = mapped
		} else if strings.Contains(path, "*") {
			t.Fatalf("route %s %s is a wildcard with no entry in starRoutes; "+
				"decide how it should be documented", r.Method, r.Path)
		} else {
			path = fiberToTemplate(path)
		}
		ops[strings.ToLower(r.Method)+" "+path] = true
	}
	return ops
}

// fiberToTemplate rewrites Fiber's `:name` parameters as OpenAPI's `{name}`.
func fiberToTemplate(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if strings.HasPrefix(seg, ":") {
			segments[i] = "{" + strings.TrimSuffix(seg[1:], "?") + "}"
		}
	}
	return strings.Join(segments, "/")
}

func documentedOperations(t *testing.T, doc openapiDoc) map[string]bool {
	t.Helper()
	ops := make(map[string]bool)
	for path, item := range doc.Paths {
		for method := range item {
			ops[strings.ToLower(method)+" "+path] = true
		}
	}
	return ops
}

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	routes := routeOperations(t)
	documented := documentedOperations(t, loadOpenAPI(t))

	var undocumented []string
	for op := range routes {
		if !documented[op] {
			undocumented = append(undocumented, op)
		}
	}
	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		t.Errorf("the gateway serves %d operation(s) that openapi.yaml does not "+
			"describe:\n\t%s\nAdd them in scripts/gen_openapi.py and regenerate.",
			len(undocumented), strings.Join(undocumented, "\n\t"))
	}
}

func TestOpenAPIDescribesNoRouteThatIsGone(t *testing.T) {
	routes := routeOperations(t)
	documented := documentedOperations(t, loadOpenAPI(t))

	var stale []string
	for op := range documented {
		if !routes[op] {
			stale = append(stale, op)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("openapi.yaml describes %d operation(s) the gateway does not "+
			"serve:\n\t%s\nRemove them from scripts/gen_openapi.py and regenerate.",
			len(stale), strings.Join(stale, "\n\t"))
	}
}

// Every operation carries an operationId and a summary. A generated client
// names its methods after the first, and an operation without the second is a
// row in the rendered reference with nothing in it.
func TestOpenAPIOperationsAreIdentified(t *testing.T) {
	doc := loadOpenAPI(t)
	seen := make(map[string]string)
	for path, item := range doc.Paths {
		for method, op := range item {
			where := strings.ToLower(method) + " " + path
			if op.OperationID == "" {
				t.Errorf("%s has no operationId", where)
			} else if prev, dup := seen[op.OperationID]; dup {
				t.Errorf("operationId %q is used by both %s and %s",
					op.OperationID, prev, where)
			} else {
				seen[op.OperationID] = where
			}
			if op.Summary == "" {
				t.Errorf("%s has no summary", where)
			}
		}
	}
}

// postman_collection.json is generated from openapi.yaml by
// scripts/gen_postman.py, which is what keeps it honest: the specification is
// already reconciled against the router above, so a collection derived from it
// cannot name an endpoint the gateway does not serve. This test is what makes
// "derived from it" a fact rather than an intention — regenerating one artefact
// and not the other is the failure it catches.

type postmanCollection struct {
	Item []postmanFolder `json:"item"`
}

type postmanFolder struct {
	Name string         `json:"name"`
	Item []postmanEntry `json:"item"`
}

type postmanEntry struct {
	Name    string `json:"name"`
	Request struct {
		Method string `json:"method"`
		URL    struct {
			Raw string `json:"raw"`
		} `json:"url"`
	} `json:"request"`
}

// collectionOperations keys each request the same way the other tests key
// theirs: "post /admin/v1/tenants/{tenant}/keys".
func collectionOperations(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "postman_collection.json"))
	if err != nil {
		t.Fatalf("read postman_collection.json: %v", err)
	}
	var doc postmanCollection
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse postman_collection.json: %v", err)
	}

	const base = "{{baseUrl}}"
	ops := make(map[string]bool)
	for _, folder := range doc.Item {
		for _, entry := range folder.Item {
			url := entry.Request.URL.Raw
			if !strings.HasPrefix(url, base) {
				t.Errorf("request %q has URL %q, which does not start with %s; a "+
					"hard-coded host is how a collection ends up pointing at a "+
					"deployment that no longer exists", entry.Name, url, base)
				continue
			}
			path := strings.TrimPrefix(url, base)
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			ops[strings.ToLower(entry.Request.Method)+" "+fiberToTemplate(path)] = true
		}
	}
	if len(ops) == 0 {
		t.Fatal("postman_collection.json contains no requests")
	}
	return ops
}

func TestPostmanCollectionMatchesTheSpecification(t *testing.T) {
	documented := documentedOperations(t, loadOpenAPI(t))
	collected := collectionOperations(t)

	var missing, extra []string
	for op := range documented {
		if !collected[op] {
			missing = append(missing, op)
		}
	}
	for op := range collected {
		if !documented[op] {
			extra = append(extra, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) > 0 {
		t.Errorf("openapi.yaml describes %d operation(s) the Postman collection "+
			"omits:\n\t%s\nRun: python scripts/gen_postman.py",
			len(missing), strings.Join(missing, "\n\t"))
	}
	if len(extra) > 0 {
		t.Errorf("the Postman collection carries %d request(s) with no operation in "+
			"openapi.yaml:\n\t%s\nRun: python scripts/gen_postman.py",
			len(extra), strings.Join(extra, "\n\t"))
	}
}
