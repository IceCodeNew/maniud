package registry

import (
	"context"
	"errors"
	"net"
	"net/http"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

var (
	// ErrCancelled reports a cancelled registry operation.
	ErrCancelled = errors.New("registry operation was cancelled")
	// ErrInvalidSource reports an invalid image source or target platform.
	ErrInvalidSource = errors.New("registry image source is invalid")
	// ErrNotFound reports an image absent from the selected registry.
	ErrNotFound = errors.New("registry image was not found")
	// ErrPlatformUnavailable reports an image without the requested platform.
	ErrPlatformUnavailable = errors.New("registry image platform is unavailable")
	// ErrProtocol reports malformed, ambiguous, or unverifiable registry content.
	ErrProtocol = errors.New("registry response is invalid")
	// ErrRateLimited reports a registry request rejected by rate limiting.
	ErrRateLimited = errors.New("registry request was rate limited")
	// ErrUnauthorized reports registry authentication or authorization failure.
	ErrUnauthorized = errors.New("registry authorization failed")
	// ErrUnavailable reports a registry transport or service failure.
	ErrUnavailable = errors.New("registry is unavailable")
)

func classifyRemoteError(err error) error {
	if errors.Is(err, context.Canceled) {
		return ErrCancelled
	}

	for _, classified := range []error{
		ErrCancelled,
		ErrNotFound,
		ErrPlatformUnavailable,
		ErrProtocol,
		ErrRateLimited,
		ErrUnauthorized,
		ErrUnavailable,
	} {
		if errors.Is(err, classified) {
			return classified
		}
	}

	if errors.Is(err, errdef.ErrNotFound) {
		return ErrNotFound
	}

	if responseError, ok := errors.AsType[*errcode.ErrorResponse](err); ok {
		return classifyResponseStatus(responseError.StatusCode)
	}

	var networkError net.Error
	if errors.As(err, &networkError) || errors.Is(err, context.DeadlineExceeded) {
		return ErrUnavailable
	}

	return ErrProtocol
}

func classifyResponseStatus(status int) error {
	switch status {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusRequestTimeout:
		return ErrUnavailable
	default:
		if status >= http.StatusInternalServerError {
			return ErrUnavailable
		}

		return ErrProtocol
	}
}
