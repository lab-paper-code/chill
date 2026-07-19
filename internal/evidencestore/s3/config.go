// Package s3 adapts one S3-compatible endpoint to the CHILL evidence-store
// contract.
package s3

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

type Config struct {
	LogicalStore    string
	Bucket          string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	TempDir         string
	HTTPClient      aws.HTTPClient
}

func New(config Config) (*Store, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	credentials := aws.Credentials{
		AccessKeyID:     config.AccessKeyID,
		SecretAccessKey: config.SecretAccessKey,
		SessionToken:    config.SessionToken,
		Source:          "CHILL Evidence Store",
	}
	awsConfig := aws.Config{
		Region:       config.Region,
		BaseEndpoint: aws.String(config.Endpoint),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return credentials, nil
		}),
		HTTPClient: config.HTTPClient,
	}
	client := awss3.NewFromConfig(awsConfig, func(options *awss3.Options) {
		options.UsePathStyle = true
	})
	return &Store{
		client: client, region: config.Region, logicalStore: config.LogicalStore,
		bucket: config.Bucket, tempDir: config.TempDir,
	}, nil
}

func (c Config) validate() error {
	for name, value := range map[string]string{
		"logical store":     c.LogicalStore,
		"bucket":            c.Bucket,
		"endpoint":          c.Endpoint,
		"region":            c.Region,
		"access key ID":     c.AccessKeyID,
		"secret access key": c.SecretAccessKey,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if strings.TrimSpace(c.Endpoint) != c.Endpoint {
		return errors.New("endpoint must not have surrounding whitespace")
	}
	if strings.TrimSpace(c.LogicalStore) != c.LogicalStore || strings.TrimSpace(c.Bucket) != c.Bucket {
		return errors.New("logical store and bucket must not have surrounding whitespace")
	}
	if c.TempDir != "" && (!filepath.IsAbs(c.TempDir) || filepath.Clean(c.TempDir) != c.TempDir) {
		return errors.New("temporary directory must be a clean absolute path")
	}
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("endpoint must be an absolute HTTP or HTTPS URL")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("endpoint must not contain user information, query, or fragment")
	}
	return nil
}
