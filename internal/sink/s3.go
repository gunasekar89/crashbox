package sink

import (
	"bytes"
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3 writes bundles to an S3 (or S3-compatible) bucket. Credentials come from
// the default AWS chain, so IRSA / EKS Pod Identity work unchanged.
// Query options: ?region=eu-west-1&endpoint=http://minio:9000&pathStyle=true
type S3 struct {
	client         *s3.Client
	bucket, prefix string
}

func NewS3(ctx context.Context, bucket, prefix string, q url.Values) (*S3, error) {
	var opts []func(*config.LoadOptions) error
	if r := q.Get("region"); r != "" {
		opts = append(opts, config.WithRegion(r))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if ep := q.Get("endpoint"); ep != "" {
			o.BaseEndpoint = aws.String(ep)
		}
		o.UsePathStyle = q.Get("pathStyle") == "true"
	})
	return &S3{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *S3) Put(ctx context.Context, key, contentType string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(join(s.prefix, key)),
		Body: bytes.NewReader(data), ContentType: aws.String(contentType),
	})
	return err
}

func (s *S3) String() string { return "s3://" + join(s.bucket, s.prefix) }
