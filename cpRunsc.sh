#!/bin/bash
#cpRunsc.sh

sudo cp /home/serverless/.cache/bazel/_bazel_serverless/f16ab6e17f68317ca3a50b71c228b779/execroot/__main__/bazel-out/k8-opt-ST-4c64f0b3d5c7/bin/runsc/runsc_/runsc /usr/local/bin
docker run --runtime=runsc ubuntu echo hello
