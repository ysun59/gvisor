![gVisor](g3doc/logo.png)

[![Build status](https://badge.buildkite.com/3b159f20b9830461a71112566c4171c0bdfd2f980a8e4c0ae6.svg?branch=master)](https://buildkite.com/gvisor/pipeline)
[![Issue reviver](https://github.com/google/gvisor/actions/workflows/issue_reviver.yml/badge.svg)](https://github.com/google/gvisor/actions/workflows/issue_reviver.yml)
[![gVisor chat](https://badges.gitter.im/gvisor/community.png)](https://gitter.im/gvisor/community)
[![code search](https://img.shields.io/badge/code-search-blue)](https://cs.opensource.google/gvisor/gvisor)



## Installing from source

gVisor builds on x86_64 and ARM64. Other architectures may become available in
the future.

For the purposes of these instructions, [bazel][bazel] and other build
dependencies are wrapped in a build container. It is possible to use
[bazel][bazel] directly, or type `make help` for standard targets.

### Requirements

Make sure the following dependencies are installed:

*   Linux 4.14.77+ ([older linux][old-linux])
*   [Docker version 17.09.0 or greater][docker]

### Building

Add current user into docker group
```sh
sudo usermod -aG docker ${USER}
reboot (or reopen the terminal)
```

Build and install the `runsc` binary:

```sh
make runsc
sudo cp ~/.cache/bazel/_bazel_serverless/f16ab6e17f68317ca3a50b71c228b779/execroot/__main__/bazel-out/k8-opt-ST-4c64f0b3d5c7/bin/runsc/runsc_/runsc /usr/local/bin
```

### Configure docker
```sh
nano /etc/docker/daemon.json
```
modify the deamon.json
```sh
{
 "runtimes": {
     "runsc": {
         "path": "/usr/local/bin/runsc"
     }
 }
}
```

```sh
sudo systemctl restart docker
```
The origin daemon.json should be like below, just for record
```sh
        "default-runtime": "custom", "runtimes": { "custom": { "path": "/usr/local/sbin/runc" } }
```
To verify runtime
```sh
docker info|grep -i runtime
```

### Testing

To verify the gvisor:

```sh
docker run --runtime=runsc ubuntu dmesg
```
or
```sh
docker run --runtime=runsc ubuntu echo hello
```
