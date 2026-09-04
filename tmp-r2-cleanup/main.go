// Temporary throwaway tool: lists (and optionally deletes) R2 objects.
// Not part of the service. Delete this directory when done.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	endpoint := os.Getenv("R2_ENDPOINT")
	bucket := os.Getenv("R2_BUCKET")

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			os.Getenv("R2_ACCESS_KEY_ID"), os.Getenv("R2_SECRET_ACCESS_KEY"), "")),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	mode := "list"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}

	if mode == "list" {
		var token *string
		total := 0
		for {
			out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
				Bucket: aws.String(bucket), ContinuationToken: token,
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, "list:", err)
				os.Exit(1)
			}
			for _, o := range out.Contents {
				fmt.Printf("%s\t%d\t%s\n", aws.ToString(o.Key), aws.ToInt64(o.Size), o.LastModified.Format(time.RFC3339))
				total++
			}
			if out.IsTruncated == nil || !*out.IsTruncated {
				break
			}
			token = out.NextContinuationToken
		}
		fmt.Fprintf(os.Stderr, "total objects in bucket: %d\n", total)
		return
	}

	if mode == "delete" {
		// Keys to delete are read from stdin, one per line. Explicit list
		// only - this never derives keys or deletes by prefix.
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			key := strings.TrimSpace(sc.Text())
			if key == "" {
				continue
			}
			_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(bucket), Key: aws.String(key),
			})
			if err != nil {
				fmt.Printf("FAILED\t%s\t%v\n", key, err)
				continue
			}
			fmt.Printf("deleted\t%s\n", key)
		}
		return
	}

	fmt.Fprintln(os.Stderr, "usage: tool [list|delete]")
	os.Exit(2)
}
