## strata serve

Serve the HTTP API and an MCP endpoint over the index

### Synopsis

Serve the JSON API under /v1 (spec at /openapi.json) and an MCP server at /mcp from
one process, which is the only one that opens the index for writing. With
--reindex-every it also re-crawls the configured registries on a schedule, so the
index stays current without webhooks. CLI commands find a running server and
route through it instead of opening the locked index file.

Bound to loopback, the server needs no auth. On any other address it is shared
mode, and refuses to start without --auth-header: the name of a header a trusted
auth proxy sets (such as X-Forwarded-User). The lineage graph maps your attack
surface, so it is never served unauthenticated beyond localhost. The API is
read-only.

```
strata serve [flags]
```

### Examples

```
  strata serve
  strata serve --reindex-every 15m
  strata serve --addr :8080 --auth-header X-Forwarded-User
```

### Options

```
      --addr string              listen address (default: 127.0.0.1:7878)
      --auth-header string       header a trusted auth proxy sets on every request; required off loopback
  -h, --help                     help for serve
      --reindex-every duration   re-crawl the configured registries this often, such as 15m (0 disables)
```

### Options inherited from parent commands

```
      --config string      config file (default: $XDG_CONFIG_HOME/strata/strata.yaml, ~/.strata.yaml or ./strata.yaml)
      --db string          index file (default: $XDG_DATA_HOME/strata/strata.duckdb)
      --log-level string   log level: debug, info, warn or error (default: info)
      --platform string    platform to pick when a ref names a multi-platform index (default: linux/amd64)
      --server string      URL of a running strata serve to query instead of the index file
```

### SEE ALSO

* [strata](strata.md)	 - Turn a container registry into a queryable lineage graph

