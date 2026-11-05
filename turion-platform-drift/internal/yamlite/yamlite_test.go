package yamlite

import (
	"testing"
)

func TestParseNestedMapping(t *testing.T) {
	doc, err := ParseOne(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ground-api
  namespace: ground-system
  labels:
    app: ground-api
spec:
  replicas: 3
`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := GetString(doc, "kind"); v != "Deployment" {
		t.Errorf("kind = %q", v)
	}
	if v, _ := GetString(doc, "metadata", "namespace"); v != "ground-system" {
		t.Errorf("namespace = %q", v)
	}
	if v, _ := GetString(doc, "metadata", "labels", "app"); v != "ground-api" {
		t.Errorf("label = %q", v)
	}
	if r, ok := Get(doc, "spec", "replicas"); !ok {
		t.Error("missing replicas")
	} else if r.(int64) != 3 {
		t.Errorf("replicas = %v", r)
	}
}

func TestParseSequenceOfMappings(t *testing.T) {
	doc, err := ParseOne(`
containers:
  - name: api
    image: registry.ground.local/api:1.0.0
    ports:
      - containerPort: 8080
  - name: sidecar
    image: registry.ground.local/sidecar:1.0.0
`)
	if err != nil {
		t.Fatal(err)
	}
	list, ok := doc.(map[string]any)["containers"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("containers = %v", doc)
	}
	first := list[0].(map[string]any)
	if first["name"] != "api" {
		t.Errorf("name = %v", first["name"])
	}
	ports, ok := first["ports"].([]any)
	if !ok || len(ports) != 1 {
		t.Fatalf("ports = %v", first["ports"])
	}
	if ports[0].(map[string]any)["containerPort"].(int64) != 8080 {
		t.Errorf("containerPort = %v", ports[0])
	}
}

func TestParseFlowCollections(t *testing.T) {
	doc, err := ParseOne(`
a: [1, 2, 3]
b: {name: x, port: 80}
c: ["drop"]
d: []
e: {}
`)
	if err != nil {
		t.Fatal(err)
	}
	m := doc.(map[string]any)
	if got := m["a"].([]any); len(got) != 3 || got[2].(int64) != 3 {
		t.Errorf("a = %v", m["a"])
	}
	b := m["b"].(map[string]any)
	if b["name"] != "x" || b["port"].(int64) != 80 {
		t.Errorf("b = %v", b)
	}
	if got := m["c"].([]any); len(got) != 1 || got[0] != "drop" {
		t.Errorf("c = %v", m["c"])
	}
	if got := m["d"].([]any); len(got) != 0 {
		t.Errorf("d = %v", m["d"])
	}
}

func TestParseScalarTypes(t *testing.T) {
	doc, err := ParseOne(`
i: 42
f: 1.5
bt: true
bf: false
n: null
s: hello
q: "quoted: colon"
sq: 'single'
`)
	if err != nil {
		t.Fatal(err)
	}
	m := doc.(map[string]any)
	if m["i"].(int64) != 42 {
		t.Errorf("i = %v (%T)", m["i"], m["i"])
	}
	if m["f"].(float64) != 1.5 {
		t.Errorf("f = %v", m["f"])
	}
	if m["bt"] != true || m["bf"] != false {
		t.Errorf("bools = %v %v", m["bt"], m["bf"])
	}
	if m["n"] != nil {
		t.Errorf("n = %v", m["n"])
	}
	if m["q"] != "quoted: colon" {
		t.Errorf("q = %q", m["q"])
	}
	if m["sq"] != "single" {
		t.Errorf("sq = %q", m["sq"])
	}
}

func TestParseComments(t *testing.T) {
	doc, err := ParseOne(`
# leading comment
kind: Service   # trailing comment
metadata:
  # nested comment
  name: api
  annotations:
    note: "value # not a comment"
`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := GetString(doc, "kind"); v != "Service" {
		t.Errorf("kind = %q", v)
	}
	if v, _ := GetString(doc, "metadata", "annotations", "note"); v != "value # not a comment" {
		t.Errorf("note = %q", v)
	}
}

func TestParseBlockScalar(t *testing.T) {
	doc, err := ParseOne(`
config:
  inline: |
    line one
    line two
  folded: >-
    a
    b
`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := doc.(map[string]any)["config"].(map[string]any)
	if cfg["inline"] != "line one\nline two\n" {
		t.Errorf("inline = %q", cfg["inline"])
	}
	if cfg["folded"] != "a b" {
		t.Errorf("folded = %q", cfg["folded"])
	}
}

func TestParseMultiDocument(t *testing.T) {
	docs, err := Parse(`
apiVersion: v1
kind: Namespace
metadata:
  name: a
---
apiVersion: v1
kind: Service
metadata:
  name: b
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d documents", len(docs))
	}
	if v, _ := GetString(docs[1].Value, "kind"); v != "Service" {
		t.Errorf("second doc kind = %q", v)
	}
}

func TestParseRejectsMalformedFlow(t *testing.T) {
	if _, err := ParseOne(`a: [1, 2`); err == nil {
		t.Error("expected an error for an unterminated flow sequence")
	}
	if _, err := ParseOne(`a: {b: 1`); err == nil {
		t.Error("expected an error for an unterminated flow mapping")
	}
}

func TestGetMissingPath(t *testing.T) {
	doc, _ := ParseOne("a:\n  b: 1\n")
	if _, ok := Get(doc, "a", "c"); ok {
		t.Error("expected missing path to report false")
	}
	if _, ok := Get(doc, "x", "y", "z"); ok {
		t.Error("expected missing path to report false")
	}
}
