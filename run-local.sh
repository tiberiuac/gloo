#!/bin/bash

kind create cluster
make push-kind-images
make build-test-chart
make glooctl-linux-amd64
_output/glooctl-linux-amd64 install gateway --file _test/gloo-"$(git describe --tags --dirty | cut -c 2-)".tgz