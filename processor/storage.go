package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type StoreConfig struct {
	Endpoint  string // host:port, e.g. localhost:9000
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

type Store struct {
	client *minio.Client
	bucket string
}

func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("creating minio client: %w", err)
	}

	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("checking bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("creating bucket: %w", err)
		}
	}

	return &Store{client: client, bucket: cfg.Bucket}, nil
}

// OriginalKey and TrackKey keep every file of a song under one prefix,
// so deleting a song is a single prefix delete.
func OriginalKey(songID, ext string) string {
	return fmt.Sprintf("songs/%s/original%s", songID, ext)
}

func TrackKey(songID, trackType, ext string) string {
	return fmt.Sprintf("songs/%s/%s%s", songID, trackType, ext)
}

// Put streams r into the bucket. Pass size -1 if unknown (minio buffers into multipart uploads).
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("uploading %s: %w", key, err)
	}
	return nil
}

// Get returns a reader for the object; the caller must close it.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting %s: %w", key, err)
	}
	return obj, nil
}

// Open returns a seekable reader for the object, for serving it with
// http.ServeContent (which needs seeking for range requests). It returns
// ErrObjectNotFound if the key doesn't exist.
func (s *Store) Open(ctx context.Context, key string) (io.ReadSeekCloser, time.Time, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("getting %s: %w", key, err)
	}
	// GetObject is lazy; Stat makes the request and tells us if the object exists
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, time.Time{}, ErrObjectNotFound
		}
		return nil, time.Time{}, fmt.Errorf("stat %s: %w", key, err)
	}
	return obj, info.LastModified, nil
}

var ErrObjectNotFound = errors.New("object not found")

// PresignedURL lets a client download/stream the file straight from minio,
// with range requests, without going through the API server.
func (s *Store) PresignedURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("presigning %s: %w", key, err)
	}
	return u.String(), nil
}

// DeleteSong removes every object under songs/<songID>/.
func (s *Store) DeleteSong(ctx context.Context, songID string) error {
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    fmt.Sprintf("songs/%s/", songID),
		Recursive: true,
	})
	for res := range s.client.RemoveObjects(ctx, s.bucket, objects, minio.RemoveObjectsOptions{}) {
		if res.Err != nil {
			return fmt.Errorf("deleting %s: %w", res.ObjectName, res.Err)
		}
	}
	return nil
}
