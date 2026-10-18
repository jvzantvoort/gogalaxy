# gogalaxy

A single binary Ansible Galaxy server. It speaks the galaxy_ng API, so
`ansible-galaxy collection publish/install/download` and
`ansible-galaxy role install/info/search/delete` work against it unchanged.
Everything is stored as plain files; there is no database and no Pulp.

Execution environments are not hosted: their API listings are empty and every
other EE request (including the `/v2/` container registry API) answers that
no execution environments are available.

## Quick start

```sh
make build
./gogalaxy user add admin --superuser            # prompts for a password
./gogalaxy user add alice --token                # prints an API token
./gogalaxy namespace create my_ns --owner alice
./gogalaxy serve                                 # listens on :8000
```

Client configuration (`ansible.cfg`):

```ini
[galaxy]
server_list = gogalaxy

[galaxy_server.gogalaxy]
url = http://localhost:8000/api/galaxy/
token = <token>
```

`url = http://localhost:8000/` works too. A repository can be targeted with
`url = http://localhost:8000/api/galaxy/content/<repository>/`. Users can
also get a token with `curl -X POST -u alice:pw http://localhost:8000/api/galaxy/v3/auth/token/`.

## Commands

| Command | Purpose |
|---|---|
| `serve` | run the API server |
| `user add/list/set/passwd/token/delete` | manage accounts and API tokens |
| `namespace create/list/add-owner/remove-owner/delete` | manage namespaces and their owners |
| `collection import/list/delete/deprecate` | manage collections without going through the API |
| `role import/list/delete` | manage roles; `role import` accepts an archive or a role directory |
| `config show` | print the effective configuration |
| `version` | print version, storage drivers and auth backends |

Management commands work directly on the storage directory and can run while
the server is up.

## Configuration

See [`gogalaxy.example.yaml`](gogalaxy.example.yaml). Values come from flags,
then `GOGALAXY_*` environment variables (`storage.path` →
`GOGALAXY_STORAGE_PATH`), then the configuration file, then defaults.

## Permissions

* Superusers may do anything.
* Namespace owners may publish, deprecate and delete content in their namespace.
* Reading is anonymous unless `auth.anonymous_read` is `false`. ansible-galaxy
  downloads roles without credentials, so role `download_url`s are then signed
  and expire after 10 minutes (set `server.secret_key` so they survive restarts).
* With `content.auto_create_namespaces` any authenticated user may create a
  namespace by uploading to it.

## Roles

galaxy_ng imports roles from GitHub. gogalaxy instead stores uploaded archives:

```sh
./gogalaxy role import ./my_role --namespace my_ns --name my_role --version 1.0.0
# or over HTTP
curl -H "Authorization: Token $TOKEN" -F file=@my_role-1.0.0.tar.gz \
     -F namespace=my_ns -F name=my_role -F version=1.0.0 \
     http://localhost:8000/api/galaxy/v1/imports/
```

## Layout

```
cmd/                    cobra commands (serve, user, namespace, collection, role, config)
internal/config         configuration struct, defaults and viper loading
internal/model          domain types
internal/storage        storage interfaces and driver registry
internal/storage/file   file based driver
internal/auth           authenticator interface/registry (token, basic), policy, URL signer
internal/service        business logic shared by API and CLI
internal/importer       collection and role archive inspection
internal/api            router; v3 (collections), v1 (roles), ee (execution environment stubs), httpx helpers
```

### Extending

* **Storage**: implement `storage.Store` and call `storage.Register("name", factory)`
  from an `init` function; select it with `storage.driver`. Driver specific
  settings go under `storage.options`.
* **Authentication**: implement `auth.Authenticator` and call
  `auth.Register("name", factory)`; add the name to `auth.backends`. Backend
  settings go under `auth.options.<name>`.
* **Authorization**: implement `auth.Authorizer` and pass it to `service.New`
  instead of the default owner based policy.
* **API**: add a package under `internal/api` and mount it in `api.NewHandler`.

## Development and releases

```sh
make build lint test     # what CI runs, roughly
make snapshot            # local dry run of a release (all platforms, nothing published)
make docker              # local container image (docker or podman)
```

GitHub Actions:

* **CI** (`.github/workflows/ci.yml`, every push and PR): `go mod tidy` check,
  golangci-lint, race tests with coverage on the go.mod and stable Go
  versions, govulncheck, and a GoReleaser snapshot build of all binaries and
  the container image. PR titles must follow
  [Conventional Commits](https://www.conventionalcommits.org/).
* **Release** (`.github/workflows/release.yml`): versions come from the
  commit messages on `main`. release-please keeps a release PR open with the
  next version and `CHANGELOG.md` (`fix:` → patch, `feat:` → minor while
  below 1.0, `feat!:` / `BREAKING CHANGE:` → major). Merging it tags
  `vX.Y.Z` and creates the GitHub release; GoReleaser then attaches archives
  for Linux, macOS, Windows and FreeBSD (amd64/arm64), checksums and SBOMs,
  and pushes `ghcr.io/jvzantvoort/gogalaxy:{X.Y.Z,X.Y,latest}`.
  Pushing a `v*` tag by hand also produces a release.
* **Dependabot** keeps Go modules, actions and the base image current.

Container usage:

```sh
docker run -d -p 8000:8000 -v gogalaxy-data:/data ghcr.io/jvzantvoort/gogalaxy
docker exec -it <container> gogalaxy user add admin --superuser
```

## Storage layout

```
data/users/<username>.json
data/tokens/<sha256 of token>.json
data/namespaces/<name>.json
data/repositories/<repo>/<namespace>/<name>/collection.json
data/repositories/<repo>/<namespace>/<name>/versions/<version>.json
data/repositories/.generation          (token that changes on every collection write)
data/roles/<namespace>/<name>.json
data/roles/.index/<id>                 (role ID -> namespace/name, rebuilt on demand)
data/tasks/<id>.json
data/artifacts/{collections,roles}/<file>.tar.gz
```

Writes are atomic (temporary file + rename), so reads take no lock; writes
are serialised within one process. Role download counts are buffered in
memory and written every 10 seconds and on shutdown.

`serve` keeps an in-memory index of collections (`storage.index`, on by
default) for browsing, search and version lookups. Every collection write,
from the server or a CLI command, changes `repositories/.generation`, and the
index rebuilds the affected repository on the next request, so changes are
visible immediately. If you modify the data directory by other means (restore
a backup, rsync), restart the server. Running two servers on
the same directory at once is not supported.
