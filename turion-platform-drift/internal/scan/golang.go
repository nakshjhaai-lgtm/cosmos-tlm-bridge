package scan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// awsGoService maps an aws-sdk-go-v2 sub-package to (rule, service).
var awsGoService = map[string]string{
	"lambda": "lambda", "batch": "batch", "s3": "s3", "dynamodb": "dynamodb",
	"sqs": "sqs", "sns": "sns", "sts": "sts", "rds": "rds",
	"secretsmanager": "secretsmanager", "ssm": "ssm", "cloudwatchlogs": "cloudwatch",
	"cloudwatch": "cloudwatch", "ec2": "ec2", "ecr": "ecr", "eks": "eks",
	"eventbridge": "eventbridge", "kinesis": "kinesis", "glue": "glue",
	"athena": "athena", "sfn": "stepfunctions", "xray": "xray",
}

// ScanGo analyses one Go file with the standard-library AST.
func ScanGo(root, rel string, c *Collector) {
	path := filepath.Join(root, rel)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		c.AddError("%s: parse error: %v", rel, err)
		return
	}
	scanGoFile(fset, file, rel, c)
}

// ScanGoSource analyses Go source held in memory. Exported for fuzzing and for
// callers that already have the bytes.
func ScanGoSource(rel string, src []byte, c *Collector) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		c.AddError("%s: parse error: %v", rel, err)
		return
	}
	scanGoFile(fset, file, rel, c)
}

// scanGoFile runs every Go check over one parsed file.
func scanGoFile(fset *token.FileSet, file *ast.File, rel string, c *Collector) {
	awsImports := map[string]string{} // local alias -> service

	for _, imp := range file.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if !strings.Contains(p, "amazonaws.com") && !strings.Contains(p, "aws-sdk-go") &&
			!strings.Contains(p, "aws/aws-sdk-go") {
			continue
		}
		service := "aws"
		if i := strings.LastIndex(p, "/"); i >= 0 {
			if s, ok := awsGoService[p[i+1:]]; ok {
				service = s
			} else {
				service = p[i+1:]
			}
		}
		alias := service
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		awsImports[alias] = service
		line := fset.Position(imp.Pos()).Line
		f := New("TPD020", rel, line, LangGo, "import "+p, service,
			"imports the proprietary AWS SDK package "+p)
		f.Column = fset.Position(imp.Pos()).Column
		c.Add(f)
	}

	if len(awsImports) == 0 {
		// Still look for literal ARNs and hard-coded buckets.
		scanGoLiterals(fset, file, rel, c)
		return
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					if service, isAWS := awsImports[id.Name]; isAWS {
						// lambda.NewFromConfig / s3.NewFromConfig / batch.NewClient ...
						if strings.HasPrefix(sel.Sel.Name, "New") {
							pos := fset.Position(node.Pos())
							rule := "TPD021"
							switch service {
							case "lambda":
								rule = "TPD021"
							case "batch":
								rule = "TPD004"
							case "s3":
								rule = "TPD021"
							}
							f := New(rule, rel, pos.Line, LangGo,
								id.Name+"."+sel.Sel.Name, service,
								"constructs an AWS "+service+" client")
							f.Column = pos.Column
							c.Add(f)
						}
					}
				}
				// (*client).Invoke / SubmitJob
				switch sel.Sel.Name {
				case "Invoke", "InvokeAsync":
					pos := fset.Position(node.Pos())
					if usesAwsImport(sel.X, awsImports) || strings.Contains(strings.ToLower(exprString(sel.X)), "lambda") {
						f := New("TPD003", rel, pos.Line, LangGo, sel.Sel.Name, "lambda",
							"invokes an AWS Lambda function")
						f.Column = pos.Column
						c.Add(f)
					}
				case "SubmitJob", "TerminateJob", "CancelJob", "RegisterJobDefinition":
					pos := fset.Position(node.Pos())
					f := New("TPD004", rel, pos.Line, LangGo, sel.Sel.Name, "batch",
						"submits work to AWS Batch")
					f.Column = pos.Column
					c.Add(f)
				case "AssumeRole":
					pos := fset.Position(node.Pos())
					f := New("TPD008", rel, pos.Line, LangGo, sel.Sel.Name, "sts",
						"assumes an AWS IAM role")
					f.Column = pos.Column
					c.Add(f)
				}
			}
		}
		return true
	})

	scanGoLiterals(fset, file, rel, c)
}

func usesAwsImport(expr ast.Expr, imports map[string]string) bool {
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if _, found := imports[id.Name]; found {
				return true
			}
		}
		return true
	})
	// ast.Inspect cannot early-exit, so fall back to a string probe.
	s := exprString(expr)
	for alias := range imports {
		if strings.Contains(s, alias+".") {
			return true
		}
	}
	return false
}

func exprString(e ast.Expr) string {
	var b strings.Builder
	_ = formatNode(&b, e)
	return b.String()
}

// formatNode renders an expression to a compact string.
func formatNode(b *strings.Builder, e ast.Expr) error {
	switch v := e.(type) {
	case *ast.Ident:
		b.WriteString(v.Name)
	case *ast.SelectorExpr:
		_ = formatNode(b, v.X)
		b.WriteString(".")
		b.WriteString(v.Sel.Name)
	case *ast.CallExpr:
		_ = formatNode(b, v.Fun)
		b.WriteString("()")
	case *ast.StarExpr:
		b.WriteString("*")
		_ = formatNode(b, v.X)
	case *ast.UnaryExpr:
		b.WriteString(v.Op.String())
		_ = formatNode(b, v.X)
	case *ast.ParenExpr:
		_ = formatNode(b, v.X)
	}
	return nil
}

func scanGoLiterals(fset *token.FileSet, file *ast.File, rel string, c *Collector) {
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		pos := fset.Position(lit.Pos())
		if svc, found := matchArn(value); found {
			f := New("TPD007", rel, pos.Line, LangGo, Truncate(value, 60), svc,
				"hard-codes an "+svc+" ARN literal")
			f.Column = pos.Column
			c.Add(f)
		}
		if bucket, found := matchS3URI(value); found {
			f := New("TPD005", rel, pos.Line, LangGo, Truncate(value, 60), "s3",
				"hard-codes the S3 bucket '"+bucket+"'")
			f.Column = pos.Column
			c.Add(f)
		}
		if strings.Contains(value, "dkr.ecr") && strings.Contains(value, "amazonaws.com") {
			f := New("TPD042", rel, pos.Line, LangGo, Truncate(value, 60), "ecr",
				"references an Amazon ECR image")
			f.Column = pos.Column
			c.Add(f)
		}
		return true
	})
}
