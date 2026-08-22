package registry

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

func TestClassifyRemoteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "cancelled", err: context.Canceled, want: ErrCancelled},
		{name: "protocol", err: ErrProtocol, want: ErrProtocol},
		{name: "not found", err: errdef.ErrNotFound, want: ErrNotFound},
		{name: "response not found", err: responseError(http.StatusNotFound), want: ErrNotFound},
		{name: "unauthorized", err: responseError(http.StatusUnauthorized), want: ErrUnauthorized},
		{name: "forbidden", err: responseError(http.StatusForbidden), want: ErrUnauthorized},
		{name: "rate limited", err: responseError(http.StatusTooManyRequests), want: ErrRateLimited},
		{name: "request timeout", err: responseError(http.StatusRequestTimeout), want: ErrUnavailable},
		{name: "server failure", err: responseError(http.StatusBadGateway), want: ErrUnavailable},
		{name: "bad response", err: responseError(http.StatusBadRequest), want: ErrProtocol},
		{name: "deadline", err: context.DeadlineExceeded, want: ErrUnavailable},
		{name: "network", err: &net.DNSError{Err: "failed", Name: "registry.example"}, want: ErrUnavailable},
		{name: "unknown", err: io.ErrNoProgress, want: ErrProtocol},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := classifyRemoteError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("classifyRemoteError() = %v, want %v", got, test.want)
			}
		})
	}
}

func responseError(status int) error {
	return &errcode.ErrorResponse{
		Method:     http.MethodGet,
		URL:        &url.URL{Scheme: "https", Host: "registry.example"},
		StatusCode: status,
	}
}
