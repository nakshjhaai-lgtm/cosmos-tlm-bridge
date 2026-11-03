// Package main is the Go ground-service ETL. It imports the proprietary AWS
// SDK directly, which the drift linter converts into a portable Job.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const archiveBucket = "turion-tlm-archive-prod"

func main() {
	ctx := context.Background()
	cfg, _ := config.LoadDefaultConfig(ctx, config.WithRegion("us-west-2"))

	// Submit a Batch job.
	bc := batch.NewFromConfig(cfg)
	out, err := bc.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName:       strPtr(os.Getenv("PASS_ID")),
		JobQueue:      strPtr("arn:aws:batch:us-west-2:123456789012:job-queue/droid-pass-processing"),
		JobDefinition: strPtr("arn:aws:batch:us-west-2:123456789012:job-definition/pass-processor:7"),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("submitted", *out.JobId)

	// Read the archive bucket.
	sc := s3.NewFromConfig(cfg)
	_, err = sc.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: strPtr(archiveBucket)})
	if err != nil {
		panic(err)
	}

	// Assume the ETL role.
	stc := sts.NewFromConfig(cfg)
	_, err = stc.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         strPtr("arn:aws:iam::123456789012:role/droid-etl-runner"),
		RoleSessionName: strPtr("etl"),
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(url.PathEscape("s3://" + archiveBucket + "/passes/"))
}

func strPtr(s string) *string { return &s }
