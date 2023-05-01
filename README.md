
# Network Quality Go Server

## Welcome!
The _Network Quality Go Server_ project originally started out as part of the [_Network Quality Server_](https://github.com/network-quality/server) project. For ease of integration with other projects, it was decided this project would be better served by being it's own repo/project.

## Contributing
Please review [_how to contribute_](CONTRIBUTING.md) if you would like to submit a pull request.

## Asking Questions and Discussing Ideas
If you have any questions you’d like to ask publicly, or ideas you’d like to discuss, please [_raise a GitHub issue_](https://github.com/network-quality/goserver/issues).
##
## Project Maintenance
Project maintenance involves, but is not limited to, adding clarity to incoming [_issues_](https://github.com/network-quality/goserver/issues) and reviewing pull requests. Project maintainers can approve and merge pull requests. Reviewing a pull request involves judging that a proposed contribution follows the project’s guidelines, as described by the [_guide to contributing_](CONTRIBUTING.md).

Project maintainers are expected to always follow the project’s [_Code of Conduct_](CODE_OF_CONDUCT.md), and help to model it for others.

## Project Governance
Although we expect this to happen very infrequently, we reserve the right to make changes, including changes to the configuration format and scope, to the project at any time.


## Building (requires Go 1.19+)

`make`

or

`go install ./...`

## Running

### Usage:

```
Usage of ./networkqualityd:
  -announce
        announce this server using DNS-SD
  -cert-file string
        cert to use
  -config-name string
        domain to generate config for (default "networkquality.example.com")
  -context-path string
        context-path if behind a reverse-proxy
  -create-cert
        generate self-signed certs
  -debug
        enable debug mode
  -enable-cors
        enable CORS headers
  -enable-h2c
        enable h2c (non-TLS http/2 prior knowledge) mode
  -enable-http2
        enable HTTP/2 (default true)
  -enable-http3
        enable HTTP/3
  -insecure-public-port int
        The port to listen on for HTTP measurement accesses
  -key-file string
        key to use
  -listen-addr string
        address to bind to (default "localhost")
  -public-name string
        host to generate config for (same as -config-name if not specified)
  -public-port int
        The port to listen on for HTTPS/H2C/HTTP3 measurement accesses (default 4043)
  -socket-send-buffer-size uint
        The size of the socket send buffer via TCP_NOTSENT_LOWAT. Zero/unset means to leave unset
  -tos string
        set TOS for listening socket (default "0")
  -version
        Show version
```

### Example run:

```
./networkqualityd --create-cert --public-name networkquality.example.com
2023/05/01 10:38:56 Network Quality URL: https://networkquality.example.com:4043/.well-known/nq
2023/05/01 10:38:56 Enabling H2 on "localhost:4043"
```

#### Running Apple's client against server:
```
networkQuality -C https://networkquality.example.com:4043/.well-known/nq -k
==== SUMMARY ====
Uplink capacity: 1.649 Gbps
Downlink capacity: 4.933 Gbps
Responsiveness: Medium (606 RPM)
Idle Latency: 3.917 milliseconds
```

#### Running the goresponsiveness client against server:
From the [gorespsonsiveness](https://github.com/network-quality/goresponsiveness) checkout
```
go run networkQuality.go --url https://networkquality.example.com:4043/.well-known/nq --insecure-skip-verify
05-01-2023 17:42:14 UTC Go Responsiveness to networkquality.example.com:4043...
RPM:  1197 (P90)
RPM:  1907 (Double-Sided 10% Trimmed Mean)
Download: 6038.648 Mbps (754.831 MBps), using 9 parallel connections.
Upload:   1561.561 Mbps (195.195 MBps), using 9 parallel connections.
```


## Docker

The server can be run in a docker container. The `Dockerfile` in this repository
will generate a container that can run the server. To build the container,
simply execute

```
docker build -t rpmserver .
```

The command will generate a container image named `rpmserver`.

In order to run the resulting container, you will have to either accept some
default values or provide configuration. The server requires access
to a public/private key for its SSL connections, and the `Dockerfile` does not
specify that they be copied in to the image. In other words, you will have to configure a
shared volume between the executing container and the host, where that volume
contains the key files.

The container executing the RPM server will also need to have a port map
established. You will have to publish the port on which the server in the
container is listening to the host.

Assuming that you use the default values specified in the `Dockerfile`, you can
run the container using

```
docker run --env-file docker_config.env  -v $(pwd)/live:/live -p 4043:4043 -p rpmserver
```

where there exists a directory `$(pwd)/live` that contains two files named
`fullchain.pem` and `privkey.pem` that hold the public and private keys for
the SSL connections, respectively.

You can use environment variables to configure any of the `networkqualityd` command-line options.

| Command-line option name | Environment variable name |
| -- | -- |
| `-cert-file` | `cert_file` |
| `-key-file` | `key_file` |
| `-public-port` | `public_port` |
| `-config-name` | `config_name` |
| `-listen-addr` | `listen_addr` |
| `-public-name` | `public_name` |
| `-debug` | *see below* |

If you want to configure whether the server runs in debug mode, simply set the `debug` environment variable to `-debug`. If you enable debugging, you will also need to create a map between a port on the host and port 9090 on the container (e.g., `-p 9090:9090`).

There is `docker_config.env` in this directory that you can
use to make passing those configuration options to the container
easier. To use this file, add the `--env-file docker_config.env` arguments to the `docker run` command.
