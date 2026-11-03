// Command clean-service is a portable ground microservice: it talks to an
// S3-compatible endpoint read from configuration and imports no AWS SDK.
//
// Historic note: this service used github.com/aws/aws-sdk-go-v2/service/s3 and
// read from s3://turion-tlm-archive-prod. Both were removed.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	endpoint := os.Getenv("OBJECT_STORE_ENDPOINT")
	if endpoint == "" {
		panic("OBJECT_STORE_ENDPOINT is required")
	}
	resp, err := http.Get(endpoint + "/health")
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	fmt.Println("object store healthy:", resp.StatusCode)
}
