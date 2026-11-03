// Separate module so the parent linter module never tries to build the
// intentionally AWS-coupled fixture. The scanner analyses it syntactically.
module github.com/turion-space/ground-service

go 1.22

require (
	github.com/aws/aws-sdk-go-v2/config v1.27.27
	github.com/aws/aws-sdk-go-v2/service/batch v1.44.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.58.2
	github.com/aws/aws-sdk-go-v2/service/sts v1.30.3
)
