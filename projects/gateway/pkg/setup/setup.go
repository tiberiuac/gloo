package setup

import (
	"context"

	"github.com/solo-io/gloo-edge/pkg/version"

	"github.com/solo-io/gloo-edge/pkg/utils/setuputils"
	"github.com/solo-io/gloo-edge/projects/gateway/pkg/syncer"
)

func Main(customCtx context.Context) error {
	return setuputils.Main(setuputils.SetupOpts{
		LoggerName:  "gateway",
		Version:     version.Version,
		SetupFunc:   syncer.Setup,
		ExitOnError: true,
		CustomCtx:   customCtx,
	})
}
