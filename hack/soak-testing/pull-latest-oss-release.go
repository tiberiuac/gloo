package main

import (
	"fmt"
	"github.com/google/go-github/v31/github"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/rotisserie/eris"
	"github.com/solo-io/gloo/docs/cmd/changelogutils"
	"github.com/solo-io/go-utils/log"
	"os"
	"os/exec"
)

var (
	pushGatewayAddr = "http://localhost:9091"
)

func main() {
	err := updateGlooRelease()
	if err != nil {
		log.Fatalf("err in main: %v", err.Error())
		os.Exit(1)
	}
	err = pushCompletionTimeToPrometheus()
	if err != nil {
		log.Fatalf("err in main: %v", err.Error())
		os.Exit(1)
	}
}

func pushCompletionTimeToPrometheus() error {
	completionTime := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gloo_upgrade_cron_job_last_completion_time_seconds",
		Help: "The timestamp of the last successful completion of a gloo upgrade.",
	})
	completionTime.SetToCurrentTime()
	if err := push.New(pushGatewayAddr, "gloo_upgrade").
		Collector(completionTime).
		Grouping("db", "customers").
		Push(); err != nil {
		return eris.Wrap(err, "Could not push completion time to prometheus push gateway")
	}
	return nil
}

func updateGlooRelease() error {
	client := github.NewClient(nil)
	repo := "gloo"
	allReleases, err := changelogutils.GetAllReleases(client, repo)
	if err != nil {
		return eris.Wrap(err, "unable to get releases from gloo github")
	}
	allReleases = changelogutils.SortReleases(allReleases)
	mostRecentRelease := *allReleases[0].Name
	releaseArg := fmt.Sprintf("--release=%s", mostRecentRelease)
	out, err := exec.Command("glooctl", "upgrade", releaseArg).CombinedOutput()
	if err != nil {
		return eris.Wrapf(err, "unable to upgrade gloo to %s\n Gloo output: %s", mostRecentRelease, string(out))
	}
	return nil
}
