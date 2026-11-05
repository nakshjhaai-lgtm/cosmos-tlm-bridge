package main

import (
	"testing"

	"turion-platform-drift/internal/generate"
	"turion-platform-drift/internal/scan"
	"turion-platform-drift/internal/yamlite"
)

func BenchmarkScanGroundService(b *testing.B) {
	for i := 0; i < b.N; i++ {
		res, err := scan.Run(scan.Options{Root: "ground-service"})
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Findings) == 0 {
			b.Fatal("no findings")
		}
	}
}

func BenchmarkScanManifest(b *testing.B) {
	src := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/api
spec:
  template:
    spec:
      containers:
        - name: c
          image: 123456789012.dkr.ecr.us-west-2.googleapis.com/x:1
          env:
            - name: BUCKET
              value: s3://turion-tlm-archive-prod
`
	for i := 0; i < b.N; i++ {
		col := scan.NewCollector()
		scan.ScanText("m.yaml", src, scan.LangYAML, col)
		scan.ScanManifest("m.yaml", src, scan.LangYAML, col)
	}
}

func BenchmarkYAMLiteParseBundle(b *testing.B) {
	// A realistic bundle: many documents, nested maps and lists.
	doc := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  labels:
    a: b
spec:
  replicas: 2
  selector:
    matchLabels:
      app: api
  template:
    spec:
      containers:
        - name: c
          image: x:1
          ports:
            - {name: http, containerPort: 8080}
          env:
            - name: A
              value: "1"
---
`
	src := ""
	for i := 0; i < 40; i++ {
		src += doc
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docs, err := yamlite.Parse(src)
		if err != nil {
			b.Fatal(err)
		}
		if len(docs) == 0 {
			b.Fatal("no docs")
		}
	}
}

func BenchmarkGenerate(b *testing.B) {
	res, err := scan.Run(scan.Options{Root: "ground-service"})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := generate.Build(res, generate.Options{Target: "crossplane"}); err != nil {
			b.Fatal(err)
		}
	}
}
