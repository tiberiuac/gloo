package setup

import (
	"context"

	"github.com/solo-io/gloo-edge/pkg/version"

	"github.com/solo-io/gloo-edge/pkg/utils/setuputils"
)

func Main(customCtx context.Context) error {
	return setuputils.Main(setuputils.SetupOpts{
		LoggerName:  "ingress",
		Version:     version.Version,
		SetupFunc:   Setup,
		ExitOnError: true,
		CustomCtx:   customCtx,
	})
}
