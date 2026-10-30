## strata mcp

Run an MCP server over stdio for AI agents

### Synopsis

Run a Model Context Protocol server on stdin and stdout with tools for explain,
ancestry, descendants, query, stale and roots, so an agent can answer questions
such as "which prod images are on an EOL base?" from the index. strata serve
offers the same tools over HTTP at /mcp.

```
strata mcp [flags]
```

### Examples

```
  claude mcp add strata -- strata mcp
  strata mcp --db /var/lib/strata/strata.duckdb
```

### Options

```
  -h, --help   help for mcp
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

