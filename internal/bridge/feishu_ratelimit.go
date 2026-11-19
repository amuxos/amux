package bridge

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	feishuRateLimitCode        = 99991400
	feishuRateLimitMaxRetries  = 3
	feishuRateLimitBaseBackoff = 1 * time.Second
	feishuRateLimitMaxBackoff  = 8 * time.Second
)

// isFeishuRateLimitError reports whether err represents a Feishu API rate
// limit response (code 99991400). The Lark SDK may surface this as a
// structured CodeError via feishuAPIResponseError or as a plain string
// error from iterator Next() calls.
func isFeishuRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	if permErr, ok := err.(*feishuPermissionError); ok && permErr.code == feishuRateLimitCode {
		return true
	}
	return strings.Contains(err.Error(), "99991400")
}

// retryFeishuRateLimit calls fn up to feishuRateLimitMaxRetries+1 times,
// retrying only when the error is a Feishu rate limit (code 99991400).
// Between retries it sleeps with exponential backoff, respecting ctx.
func retryFeishuRateLimit(ctx context.Context, operation string, fn func() error) error {
	return retryFeishuRateLimitWithBackoff(ctx, operation, feishuRateLimitBaseBackoff, feishuRateLimitMaxBackoff, fn)
}

func retryFeishuRateLimitWithBackoff(ctx context.Context, operation string, baseBackoff, maxBackoff time.Duration, fn func() error) error {
	backoff := baseBackoff
	var lastErr error
	for attempt := 0; attempt <= feishuRateLimitMaxRetries; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if !isFeishuRateLimitError(err) || attempt == feishuRateLimitMaxRetries {
				return err
			}
			log.Warn().Err(err).Str("operation", operation).Int("attempt", attempt+1).
				Dur("backoff", backoff).
				Msg("bridge: feishu rate limit hit, retrying after backoff")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		return nil
	}
	return lastErr
}
