// Package storage is a minimal Supabase Storage REST client: upload, delete,
// and public-URL construction. The server is the only writer (secret key);
// reads go straight from browsers to the public bucket URLs.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CacheOneYear is the Cache-Control max-age for uploaded objects: seconds in
// one year. Safe because object paths are content-addressed by UUID and never
// rewritten.
const CacheOneYear = 365 * 24 * 60 * 60

type Client struct {
	baseURL   string
	secretKey string
	http      *http.Client
}

func New(supabaseURL, secretKey string) *Client {
	return &Client{
		baseURL:   supabaseURL,
		secretKey: secretKey,
		http:      &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) objectURL(bucket, path string) string {
	return fmt.Sprintf("%s/storage/v1/object/%s/%s", c.baseURL, bucket, path)
}

// PublicURL returns the browser-facing URL for an object in a public bucket.
// The frontend never builds these itself.
func (c *Client) PublicURL(bucket, path string) string {
	return fmt.Sprintf("%s/storage/v1/object/public/%s/%s", c.baseURL, bucket, path)
}

func (c *Client) Upload(ctx context.Context, bucket, path, contentType string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.objectURL(bucket, path), body)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.secretKey)
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Cache-Control", fmt.Sprintf("max-age=%d", CacheOneYear))

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("storage upload %s/%s: status %d: %s", bucket, path, resp.StatusCode, msg)
	}
	return nil
}

func (c *Client) Delete(ctx context.Context, bucket, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.objectURL(bucket, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.secretKey)
	req.Header.Set("Authorization", "Bearer "+c.secretKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("storage delete %s/%s: status %d: %s", bucket, path, resp.StatusCode, msg)
	}
	return nil
}
