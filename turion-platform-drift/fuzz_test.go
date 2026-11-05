package main

// Fuzz targets: the scanner parses untrusted source code, so it must never
// panic on arbitrary bytes. Run with:
//   go test -fuzz=FuzzScanText -fuzztime=60s .

import (
	"strings"
	"testing"

	"turion-platform-drift/internal/scan"
)

func FuzzScanText(f *testing.F) {
	seeds := []string{
		"import boto3\nc = boto3.client('s3')\n",
		"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: x\n",
		"resource \"aws_s3_bucket\" \"b\" {}\n",
		"import { S3Client } from '@aws-sdk/client-s3';\n",
		"aws s3 sync . s3://b\n",
		"",
		"\x00\x01\x02",
		"{{ .Values.x }}",
		strings.Repeat("[", 64),
	}
	for _, s := range seeds {
		f.Add(s, "yaml")
	}
	f.Fuzz(func(t *testing.T, content string, lang string) {
		l := scan.Language(lang)
		switch l {
		case scan.LangYAML, scan.LangJSON, scan.LangHCL, scan.LangTypeScript, scan.LangShell, scan.LangJSONNet:
		default:
			l = scan.LangYAML
		}
		col := scan.NewCollector()
		scan.ScanText("fuzz.yaml", content, l, col)
		scan.ScanManifest("fuzz.yaml", content, l, col)
		for _, fnd := range col.Findings() {
			if fnd.RuleID == "" {
				t.Fatalf("finding with no rule id from input %q", content)
			}
			if fnd.Line <= 0 {
				t.Fatalf("finding with line %d from input %q", fnd.Line, content)
			}
			if fnd.File == "" {
				t.Fatalf("finding with no file from input %q", content)
			}
		}
	})
}

func FuzzScanGoSource(f *testing.F) {
	f.Add("package m\n\nimport \"github.com/aws/aws-sdk-go-v2/service/s3\"\n")
	f.Add("package m\n\nfunc f() {}\n")
	f.Add("not go at all")
	f.Fuzz(func(t *testing.T, src string) {
		col := scan.NewCollector()
		// ScanGoSource must tolerate any input: a parse failure is fine, a
		// panic is not.
		scan.ScanGoSource("fuzz.go", []byte(src), col)
		_ = col.Findings()
	})
}

func FuzzPythonAnalyzer(f *testing.F) {
	seeds := []string{
		"import boto3\n",
		"def f():\n    pass\n",
		"'''docstring mentioning s3://bucket'''\n",
		"import os\nclient = make_client(os.environ['X'])\n",
		"lambda x: x\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		// The embedded analyzer must never hang or panic on valid-looking
		// Python; syntax errors must come back as clean errors.
		findings, errs, ok := scan.AnalyzePythonSource("fuzz.py", src)
		if !ok {
			return // analyzer unavailable: covered by other tests
		}
		for _, e := range errs {
			if strings.Contains(e, "panic") {
				t.Fatalf("analyzer panicked on %q: %s", src, e)
			}
		}
		for _, fnd := range findings {
			if fnd.RuleID == "" || fnd.Line <= 0 {
				t.Fatalf("malformed finding %+v from %q", fnd, src)
			}
		}
	})
}
