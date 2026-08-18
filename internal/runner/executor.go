package runner

import (
	"context"
	"encoding/json"

	"github.com/rvben/werkt/internal/domain"
)

// Executor is the isolation boundary between Werkt's orchestration plane and
// the environment that runs automation code.
type Executor interface {
	Execute(context.Context, domain.RunnableRun) (Result, error)
}

type Result struct {
	Output json.RawMessage
	Logs   string
}
