// Copyright 2021 Saferwall. All rights reserved.
// Use of this source code is governed by Apache v2 license
// license that can be found in the LICENSE file.

package s3

import (
	"context"
	"io"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

// Service provides abstraction to cloud object storage.
type Service struct {
	// S3 uploader.
	uploader *s3manager.Uploader
}

// New generates new s3 object storage service.
func New(region, accessKey, secretKey string) (Service, error) {

	// The session the S3 Uploader will use.
	creds := credentials.NewStaticCredentials(accessKey, secretKey, "")
	sess, err := session.NewSession(&aws.Config{Region: aws.String(region),
		Credentials: creds})
	if err != nil {
		return Service{}, nil
	}

	// S3 service client the Upload manager will use.
	s3Svc := awss3.New(sess)

	// Create an uploader with S3 client and custom options
	uploader := s3manager.NewUploaderWithClient(s3Svc, func(u *s3manager.Uploader) {
		u.PartSize = 5 * 1024 * 1024 // 5MB per part
		u.LeavePartsOnError = true   // Don't delete the parts if the upload fails.
	})

	return Service{uploader}, nil
}

// Upload upload an object to s3.
func (s Service) Upload(bucket, key string, file io.Reader, timeout int) error {

	// Create a context with a timeout that will abort the upload if it takes
	// more than the passed in timeout.
	ctx := context.Background()
	var cancelFn func()
	if timeout > 0 {
		ctx, cancelFn = context.WithTimeout(ctx, time.Duration(timeout))
	}

	// Ensure the context is canceled to prevent leaking.
	// See context package for more information, https://golang.org/pkg/context/
	if cancelFn != nil {
		defer cancelFn()
	}

	// Upload input parameters
	upParams := &s3manager.UploadInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   file,
	}

	// Perform an upload.
	_, err := s.uploader.UploadWithContext(ctx, upParams)

	return err
}
