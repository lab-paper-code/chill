package s3

import (
	"context"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

type client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	DeleteObject(context.Context, *awss3.DeleteObjectInput, ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
	HeadBucket(context.Context, *awss3.HeadBucketInput, ...func(*awss3.Options)) (*awss3.HeadBucketOutput, error)
	CreateBucket(context.Context, *awss3.CreateBucketInput, ...func(*awss3.Options)) (*awss3.CreateBucketOutput, error)
}
