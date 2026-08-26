package application

import (
	"context"

	"github.com/IceCodeNew/maniud/internal/domain"
)

// OperationRuntime is one per-call runtime capability. Closing idle
// connections releases its transport after an operation completes.
type OperationRuntime interface {
	Runtime
	ProbeImage(ctx context.Context, expected domain.ImageIdentity) (ImageProbe, error)
	CloseIdleConnections()
}

// OperationRuntimeFactory opens one runtime connection after the application
// has selected a compiled runtime capability.
type OperationRuntimeFactory func(context.Context) (OperationRuntime, error)
