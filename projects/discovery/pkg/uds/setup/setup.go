package setup

import (
	"context"

	"github.com/solo-io/gloo-edge/pkg/version"

	"github.com/solo-io/gloo-edge/pkg/utils/setuputils"
	"github.com/solo-io/gloo-edge/projects/discovery/pkg/uds/syncer"
	gloosyncer "github.com/solo-io/gloo-edge/projects/gloo/pkg/syncer"
)

func Main(customCtx context.Context) error {
	return setuputils.Main(setuputils.SetupOpts{
		LoggerName:  "uds",
		Version:     version.Version,
		SetupFunc:   gloosyncer.NewSetupFuncWithRun(syncer.RunUDS),
		ExitOnError: true,
		CustomCtx:   customCtx,
	})
}
