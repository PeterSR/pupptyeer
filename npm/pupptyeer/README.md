# @petersr/pupptyeer

Install the [pupptyeer](https://github.com/PeterSR/pupptyeer) daemon, CLI, and MCP front-end as a
prebuilt binary, via npm. This package ships no JavaScript implementation: it is a thin launcher
whose `optionalDependencies` are per-platform packages (`@petersr/pupptyeer-linux-x64`,
`@petersr/pupptyeer-darwin-arm64`, `@petersr/pupptyeer-win32-x64`, ...), each carrying the static Go
binaries for one OS/arch. npm installs only the one matching your machine; the `pupptyeer` /
`pupptyeer-mcp` commands exec it.

pupptyeer is a local daemon that owns persistent PTY sessions, with a CLI and an MCP server.

## Install

```sh
# global CLI + daemon
npm i -g @petersr/pupptyeer

pupptyeer daemon install      # run the daemon as a per-user managed service
pupptyeer --help

# or run without installing
npx @petersr/pupptyeer --help
npx -p @petersr/pupptyeer pupptyeer-mcp --help
```

Prebuilt binaries are provided for linux, macOS, and Windows on x64 and arm64. On an unsupported
platform the launcher prints how to build from source.

To program against the daemon, use a client library instead:
[`pupptyeer-client`](https://www.npmjs.com/package/pupptyeer-client) (Node),
[`pupptyeer-client`](https://pypi.org/project/pupptyeer-client/) (Python), or the Go client.

## License

MIT
