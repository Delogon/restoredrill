# Recipes

A recipe is a **parameterised drill template**: a reusable restore test for a
common self-hosted app. A drill uses one with `uses:` and supplies parameters
with `with:`. Recipes are bundled with the binary or kept as local files —
there is no remote registry.

```yaml
drills:
  service1-location2:
    uses: service1            # bundled name, or a path to a recipe file
    with:
      repository: location2-borg
      data_path: /srv/service1/data
    schedule: '0 5 * * 1'
```

```sh
restoredrill recipe list                 # bundled + local recipes
restoredrill recipe show service1     # parameters and drill template
restoredrill plan service1-location2    # the fully resolved drill
```

## Bundled recipes

| Recipe | Verifies | Needs Docker |
|--------|----------|--------------|
| `generic-files` | Files exist, snapshot is fresh, restored size has not dropped | no |
| `sqlite-app` | SQLite integrity + one query against the app database | no |
| `postgres-dump` | A Postgres dump loads into a throwaway server and answers a query | yes |
| `service1` | SQLite checks, then starts the server and probes `/api/config` | yes |
| `service2` | Postgres dump loads, document originals are present | yes |
| `service3` | `configuration.yaml` exists, SQLite database is intact | no |

Every recipe takes a `repository` parameter (the name of a repository defined
in your config) and a `max_age` parameter for snapshot freshness. Run
`restoredrill recipe show <name>` for the full parameter list and defaults.

Example — the generic file drill:

```yaml
repositories:
  location1-restic:
    type: restic
    repository: /srv/restic/location1
    password_file: /etc/restoredrill/secrets/location1.pass

drills:
  documents:
    uses: generic-files
    with:
      repository: location1-restic
      data_path: /srv/documents
      min_count: 100
      max_age: 26h
```

## Overrides

Fields set on the drill override the recipe's fields: **maps deep-merge, lists
replace**. Setting `checks:` on the drill replaces the recipe's whole check
list; setting `sandbox.services.app.env` merges into the recipe's env.
`schedule`, `jitter`, `notify`, `timeout` etc. can be added freely.

## Writing your own recipe

A recipe file is YAML with five keys:

```yaml
recipe: myapp            # name (must match how you reference it)
version: 1               # schema version, always 1
description: One line shown by `recipe list`.
params:
  repository: { required: true }
  data_path:  { default: /srv/myapp/data }
  min_rows:   { default: 1 }
drill:
  repository: ${param.repository}
  snapshot:
    select: latest
    max_age: 26h
  restore:
    paths: ['${param.data_path}']
  checks:
    - type: files_exist
      paths: ['**']
      min_count: 1
```

Reference it by path (relative to the main config file) or drop it in a
`recipes/` directory next to your config and use it by name:

```yaml
drills:
  myapp:
    uses: ./recipes/myapp.yaml     # or: uses: myapp
    with:
      repository: location1-restic
```

### Parameters

- `required: true` — the drill must supply the value under `with:`.
- `default:` — used when the drill does not supply a value.
- Values are **scalars** (string, number, boolean). Numeric and boolean
  parameters work in typed fields: `min_count: ${param.min_rows}` decodes as a
  number, `catch_up: ${param.flag}` as a boolean.
- Unknown parameters (not declared in `params:`) are an error, and so are
  leftover `${param.*}` references after expansion.

### Variables

Only plain substitution, no template language:

| Variable | Resolved | Meaning |
|----------|----------|---------|
| `${param.<name>}` | config load | recipe parameter |
| `${env.<NAME>}` | config load | environment variable |
| `${restore}` | each run | absolute path of the run's restore root |
| `${run.id}` | each run | the run's ULID |

Unknown variables are a validation error.

### Paths: absolute vs relative

Restored files keep their original absolute path **below the restore root**:
backing up `/srv/app/data` produces `<restore>/srv/app/data/...`. Therefore:

- `restore.paths` and mount sources use **absolute** paths as stored in the
  backup. A mount source is typically `${restore}${param.data_path}` (no extra
  `/` — the parameter already starts with one).
- Check paths (`files_exist.paths`, `sqlite_*.path`, `postgres_restore.dump`)
  are **relative to the restore root**: the backed-up absolute path without
  the leading `/`.

Because substitution is plain text, a recipe cannot strip the leading `/`
itself. Recipes that need both forms take a pair of parameters with matching
defaults, e.g. `data_path: /srv/app/data` and `db_path: srv/app/data/app.sqlite3`.
**When you change one, change the other.** `restoredrill plan <drill>` shows
the resolved drill so you can verify the pairing; a mismatch fails the drill
(the file is not found) rather than passing silently.

Glob checks avoid the pairing entirely: `paths: ['**']` matches every restored
file and `paths: ['**/*.pdf']` matches PDFs at any depth, whatever the layout.

### YAML gotcha: `${...}` in flow collections

A `}` inside `${...}` closes a YAML flow mapping early. Do **not** write:

```yaml
expect: { min: ${param.min_rows} }     # YAML parse error
```

Use block style instead (quoting would force the value to a string and break
numeric fields):

```yaml
expect:
  min: ${param.min_rows}
```

### Tips

- Set `snapshot.max_age` in the template: it adds an implicit `snapshot_age`
  check automatically.
- Leave backend-specific selectors (`tags` for restic, `archive_glob` for
  Borg) out of shared recipes; users add them as drill-level overrides.
- Pin service images to a tag or digest; `validate` warns on `latest`.
- Keep recipes read-only: they describe restore tests, never backup jobs.