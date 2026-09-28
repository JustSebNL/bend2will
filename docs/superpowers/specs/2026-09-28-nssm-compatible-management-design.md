# NSSM-Compatible Service Management

## Goal

Bend2Will will provide a practical NSSM replacement on Windows while keeping
the existing `obey` CLI, portable supervisor, and cross-platform library.
Compatibility means the common NSSM operator workflow and parameter names, not
an attempt to reproduce every NSSM-specific registry value or GUI behavior.

The acceptance boundary is an installed service that can be configured and
managed without hand-editing a generated command line:

```text
obey install <name> <application> [arguments...]
obey start|stop|restart|status <name>
obey set <name> <parameter> <value...>
obey get <name> [parameter]
obey dump <name>
obey remove <name>
```

Existing long-form `--command`, `--arguments`, configuration-file, and
`install`/`uninstall` forms remain valid.

## Architecture

Windows service configuration will have one canonical representation: the
validated Bend2Will `Config` plus Windows install settings. Installation will
persist that representation below the service's registry key in a versioned
`Parameters\Bend2WillConfig` value. The value is JSON encoded using the
standard library, validated before writing, and never logged.

The SCM `ImagePath` will contain only the stable launcher contract:

```text
<obey.exe> service --name <service-name>
```

The hidden `service` CLI mode will load the persisted configuration and call the
existing supervisor in strict Windows service mode. A compatibility fallback
will continue to accept older definitions that embedded `--command` and other
flags in `ImagePath`.

The Windows platform will expose an optional service-editor capability rather
than expanding the portable `Platform` interface. The CLI will type-assert that
capability for `set`, `get`, and `dump`; non-Windows platforms will return a
clear unsupported-operation error and will not silently pretend to edit a
service.

## CLI compatibility

The positional install form interprets the first positional argument as the
service name, the second as the application, and the remaining arguments as
application arguments. The existing option form remains unambiguous.

The supported NSSM-style parameter mapping is:

| NSSM parameter | Bend2Will setting |
|---|---|
| `Application`, `AppPath` | supervised executable |
| `AppParameters` | supervised argument vector |
| `AppDirectory` | working directory |
| `AppStdout`, `AppStderr` | separate child output files |
| `AppEnvironmentExtra` | extra environment entries |
| `DisplayName`, `Description` | SCM metadata |
| `AppRestartDelay` | supervisor restart delay |
| `AppExit` | restart/stop policy |
| `AppStopMethodConsole`, `AppStopMethodSkip` | graceful/kill stop policy where representable |
| `DependOnService` | SCM dependencies |
| `Start` | demand, automatic, or delayed automatic start |
| `ObjectName` | supported built-in Windows service accounts |

`get` returns one parameter when named and the canonical supported settings when
no parameter is supplied. `dump` emits reproducible `obey set` commands. Unknown
or unsupported parameters fail with their name and the supported alternatives;
no setting is silently discarded.

## Windows lifecycle

SCM access will be requested per operation. Query/start/stop use connection and
the minimum service rights they need; install/remove retain configuration
rights. Start and stop will poll with a context-aware deadline so commands do
not report success while the SCM still reports a pending state. Uninstall will
return a stop timeout instead of attempting deletion of a still-running
service.

Installation will validate and persist configuration before starting the
service. If a later registry or SCM configuration step fails, the partially
created service and persisted configuration will be removed. Existing recovery
actions, delayed auto-start, service-account handling, job-object process-tree
termination, and Event Log support remain in use.

Child stdout and stderr will become independently configurable. When only one
NSSM output path is configured, only that stream is redirected; otherwise the
existing supervisor log sink and console behavior remain unchanged.

## Testing and verification

Tests will be written before each implementation slice:

1. CLI parsing for positional installation and command aliases.
2. Service-mode dispatch and persisted configuration round-trips.
3. NSSM parameter mapping, validation, `get`, and deterministic `dump` output.
4. Windows ImagePath generation and least-privilege operation selection.
5. Start/stop/uninstall pending-state behavior and context cancellation.
6. Separate child stdout/stderr routing.

The full Go test suite, `go vet`, and Windows/Linux/macOS builds will be run
after the focused tests. A live SCM smoke test will only be reported if it is
run against a disposable service name with the required administrator rights;
source and cross-build success will not be presented as proof of live service
behavior.
