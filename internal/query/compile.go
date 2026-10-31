package query

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// kind is a field's type, which decides the operators it accepts and how
// values are parsed.
type kind int

// Field kinds.
const (
	kString kind = iota
	kList
	kRef
	kInt
	kBytes
	kDays
	kTime
	kBool
	kVerdict
	// kFinding matches a column of the vulnerabilities an image's scan
	// found; expr names the column.
	kFinding
)

// Field describes one queryable field.
type Field struct {
	Name string
	Doc  string
	kind kind
	// expr is the SQL expression over image_view.
	expr string
	// present is the SQL for "this ref field has a value"; only refs set
	// it.
	present string
}

// staticFields are the fields with fixed names. label.<key>,
// missing:<key> and config.* are resolved dynamically or listed here.
var staticFields = []Field{
	{Name: "repo", Doc: "repository, including the registry host", kind: kString, expr: "repo"},
	{Name: "tag", Doc: "any tag pointing at the image (name only)", kind: kList, expr: "list_transform(tag_refs, lambda x: regexp_extract(x, ':([^:/]+)$', 1))"},
	{Name: "ref", Doc: "any repo:tag or repo@digest naming the image", kind: kList, expr: "refs"},
	{Name: "digest", Doc: "manifest digest", kind: kString, expr: "digest"},
	{Name: "index", Doc: "digest of the multi-platform index holding the image", kind: kString, expr: "index_digest"},
	{Name: "created", Doc: "creation time from the image config", kind: kTime, expr: "created"},
	{Name: "age", Doc: "days since created (accepts 180d, 12w, 1y)", kind: kDays, expr: "age_days"},
	{Name: "pushed", Doc: "when the image was first pushed, from registries that report it (ECR)", kind: kTime, expr: "pushed"},
	{Name: "pushed_age", Doc: "days since pushed (accepts 180d, 12w, 1y)", kind: kDays, expr: "pushed_age_days"},
	{Name: "first_seen", Doc: "when strata first indexed the image", kind: kTime, expr: "first_seen"},
	{Name: "size", Doc: "compressed size from the manifest (accepts 512MB, 1.5GiB)", kind: kBytes, expr: "size"},
	{Name: "layers", Doc: "number of layers", kind: kInt, expr: "layers"},
	{Name: "platform", Doc: "os/arch[/variant]", kind: kString, expr: "platform"},
	{Name: "os", Doc: "operating system", kind: kString, expr: "os"},
	{Name: "arch", Doc: "CPU architecture", kind: kString, expr: "arch"},
	{Name: "variant", Doc: "CPU variant", kind: kString, expr: "variant"},
	{Name: "external", Doc: "true for bases fetched from outside the indexed registries", kind: kBool, expr: "external"},
	{Name: "parent", Doc: "the parent image, compared against its refs", kind: kRef, expr: "parent_refs", present: "parent IS NOT NULL"},
	{Name: "root", Doc: "the root base, compared against its refs", kind: kRef, expr: "root_refs", present: "root IS NOT NULL"},
	{Name: "parent_source", Doc: "how the parent was found: layers, annotation, label", kind: kString, expr: "parent_source"},
	{Name: "generation", Doc: "number of ancestors", kind: kInt, expr: "generation"},
	{Name: "children", Doc: "number of direct children", kind: kInt, expr: "children"},
	{Name: "descendants", Doc: "number of images built on this one, at any depth", kind: kInt, expr: "descendants"},
	{Name: "tags", Doc: "number of tags pointing at the image", kind: kInt, expr: "tags"},
	{Name: "conflict", Doc: "a declared parent disagrees with the layers", kind: kBool, expr: "lineage_conflict"},
	{Name: "has:sbom", Doc: "an SBOM is attached as a referrer", kind: kBool, expr: "has_sbom"},
	{Name: "has:signature", Doc: "a signature is attached as a referrer", kind: kBool, expr: "has_signature"},
	{Name: "has:attestation", Doc: "an attestation is attached as a referrer", kind: kBool, expr: "has_attestation"},
	{Name: "stale", Doc: "verdict: fresh, drifting, stale or abandoned", kind: kVerdict, expr: "stale"},
	{Name: "stale.base_drift", Doc: "generations the parent is behind its tag", kind: kInt, expr: "stale_base_drift"},
	{Name: "stale.base_drift_days", Doc: "days since the parent was replaced", kind: kDays, expr: "stale_base_drift_days"},
	{Name: "stale.base_tag", Doc: "the tag base drift is measured against", kind: kString, expr: "stale_base_tag"},
	{Name: "stale.superseded", Doc: "days since a tag moved off this image", kind: kDays, expr: "stale_superseded_days"},
	{Name: "stale.untagged", Doc: "no tag points at the image", kind: kBool, expr: "stale_untagged"},
	{Name: "stale.last_pulled", Doc: "days since the last pull", kind: kDays, expr: "stale_last_pulled_days"},
	{Name: "stale.eol_base", Doc: "the root base is past end of life", kind: kBool, expr: "stale_eol_base"},
	{Name: "stale.label_policy", Doc: "labels mark the image deprecated or expired", kind: kBool, expr: "stale_label_policy"},
	{Name: "stale.inferred", Doc: "a signal was approximated without tag history", kind: kBool, expr: "stale_inferred"},
	{Name: "vuln.scanned", Doc: "the registry has a finished vulnerability scan of the image (ECR)", kind: kBool, expr: "vuln_scanned"},
	{Name: "vuln.scanned_at", Doc: "when the image's last scan completed", kind: kTime, expr: "vuln_scanned_at"},
	{Name: "vuln.scan_age", Doc: "days since the image's last scan completed", kind: kDays, expr: "vuln_scan_age_days"},
	{Name: "vuln.critical", Doc: "critical vulnerabilities the scan found; unset when not scanned", kind: kInt, expr: "vuln_critical"},
	{Name: "vuln.high", Doc: "high vulnerabilities the scan found", kind: kInt, expr: "vuln_high"},
	{Name: "vuln.medium", Doc: "medium vulnerabilities the scan found", kind: kInt, expr: "vuln_medium"},
	{Name: "vuln.low", Doc: "low vulnerabilities the scan found", kind: kInt, expr: "vuln_low"},
	{Name: "vuln.total", Doc: "vulnerabilities of any severity the scan found", kind: kInt, expr: "vuln_total"},
	{Name: "vuln.fixable", Doc: "vulnerabilities with a fixed version (needs scan-findings)", kind: kInt, expr: "vuln_fixable"},
	{Name: "vuln.id", Doc: "a vulnerability the scan found, such as CVE-2024-3094 (needs scan-findings)", kind: kFinding, expr: "id"},
	{Name: "vuln.package", Doc: "a package with a vulnerability, such as openssl (needs scan-findings)", kind: kFinding, expr: "package"},
	{Name: "config.user", Doc: "the user the image runs as", kind: kString, expr: "config_user"},
	{Name: "config.entrypoint", Doc: "entrypoint arguments", kind: kList, expr: "config_entrypoint"},
	{Name: "config.cmd", Doc: "cmd arguments", kind: kList, expr: "config_cmd"},
	{Name: "config.ports", Doc: "exposed ports, such as 8080/tcp", kind: kList, expr: "config_ports"},
	{Name: "config.env_keys", Doc: "environment variable names (values are never stored)", kind: kList, expr: "config_env_keys"},
	{Name: "config.workdir", Doc: "working directory", kind: kString, expr: "config_workdir"},
	{Name: "config.stop_signal", Doc: "stop signal", kind: kString, expr: "config_stop_signal"},
	{Name: "config.healthcheck", Doc: "a healthcheck is defined", kind: kBool, expr: "config_healthcheck"},
}

// fieldIndex maps names to static fields.
var fieldIndex = func() map[string]Field {
	m := map[string]Field{}
	for _, f := range staticFields {
		m[f.Name] = f
	}
	return m
}()

// Fields returns every static field plus the label.<key> and
// missing:<key> patterns, sorted by name, for help and docs.
func Fields() []Field {
	out := append([]Field{}, staticFields...)
	out = append(out,
		Field{Name: "label.<key>", Doc: "any config label or manifest annotation", kind: kString},
		Field{Name: "missing:<key>", Doc: "the label is not set", kind: kBool},
	)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup resolves a field name, including label.<key> and missing:<key>.
func Lookup(name string) (Field, error) {
	if f, ok := fieldIndex[strings.ToLower(name)]; ok {
		return f, nil
	}
	if key, ok := strings.CutPrefix(name, "label."); ok && key != "" {
		return Field{Name: name, kind: kString, expr: "labels[" + quote(key) + "]::VARCHAR"}, nil
	}
	if key, ok := strings.CutPrefix(name, "missing:"); ok && key != "" {
		return Field{Name: name, kind: kBool, expr: "(labels[" + quote(key) + "] IS NULL)"}, nil
	}
	return Field{}, fmt.Errorf("unknown field %q (see strata query --help for the list)", name)
}

// Expr returns the SQL expression for a field, for use in SELECT, GROUP BY
// and ORDER BY. Ref fields return the parent or root digest.
func Expr(name string) (string, error) {
	f, err := Lookup(name)
	if err != nil {
		return "", err
	}
	switch f.Name {
	case "parent":
		return "parent", nil
	case "root":
		return "root", nil
	}
	if f.kind == kFinding {
		return "", fmt.Errorf("%s can filter but not be selected or grouped by", f.Name)
	}
	return f.expr, nil
}

// IsList reports whether a field holds several values per image, so a
// group-by over it must unnest.
func IsList(name string) bool {
	f, err := Lookup(name)
	return err == nil && f.kind == kList
}

// Compiled is a WHERE clause and its positional arguments.
type Compiled struct {
	Where string
	Args  []any
}

// compiler accumulates arguments while walking the tree.
type compiler struct {
	args []any
}

// Compile parses and compiles a filter. An empty filter compiles to
// "true".
func Compile(src string) (Compiled, error) {
	n, err := Parse(src)
	if err != nil {
		return Compiled{}, err
	}
	return CompileNode(n)
}

// CompileNode compiles a parsed filter.
func CompileNode(n Node) (Compiled, error) {
	if n == nil {
		return Compiled{Where: "true"}, nil
	}
	c := &compiler{}
	w, err := c.node(n)
	if err != nil {
		return Compiled{}, err
	}
	return Compiled{Where: w, Args: c.args}, nil
}

// arg adds a positional argument and returns its placeholder.
func (c *compiler) arg(v any) string {
	c.args = append(c.args, v)
	return "?"
}

// node compiles one node.
func (c *compiler) node(n Node) (string, error) {
	switch n := n.(type) {
	case And:
		return c.binary(n.L, n.R, "AND")
	case Or:
		return c.binary(n.L, n.R, "OR")
	case Not:
		x, err := c.node(n.X)
		if err != nil {
			return "", err
		}
		return "(NOT coalesce(" + x + ", false))", nil
	case Compare:
		return c.compare(n)
	case In:
		return c.in(n)
	case Exists:
		return c.exists(n)
	case Flag:
		return c.flag(n)
	case Call:
		return c.call(n)
	}
	return "", fmt.Errorf("unsupported node %T", n)
}

// binary compiles a boolean connective.
func (c *compiler) binary(l, r Node, op string) (string, error) {
	ls, err := c.node(l)
	if err != nil {
		return "", err
	}
	rs, err := c.node(r)
	if err != nil {
		return "", err
	}
	return "(" + ls + " " + op + " " + rs + ")", nil
}

// fieldAt resolves a field name, attaching the position to errors.
func fieldAt(name string, pos int) (Field, error) {
	f, err := Lookup(name)
	if err != nil {
		return f, &SyntaxError{pos, err.Error()}
	}
	return f, nil
}

// compare compiles field op value.
func (c *compiler) compare(n Compare) (string, error) {
	f, err := fieldAt(n.Field, n.Pos)
	if err != nil {
		return "", err
	}
	bad := func() error {
		return &SyntaxError{n.Pos, fmt.Sprintf("operator %s does not apply to %s", n.Op, f.Name)}
	}
	switch f.kind {
	case kFinding:
		ph := c.arg(n.Value.Text)
		switch n.Op {
		case "=":
			return findingIn(f.expr + " = " + ph), nil
		case "!=":
			return "(NOT " + findingIn(f.expr+" = "+ph) + ")", nil
		case "~":
			return findingIn(f.expr + " GLOB " + ph), nil
		}
		return "", bad()
	case kList, kRef:
		ph := c.arg(n.Value.Text)
		switch n.Op {
		case "=":
			return "list_contains(" + f.expr + ", " + ph + ")", nil
		case "!=":
			return "(NOT list_contains(" + f.expr + ", " + ph + "))", nil
		case "~":
			return "(len(list_filter(" + f.expr + ", lambda x: x GLOB " + ph + ")) > 0)", nil
		}
		return "", bad()
	case kBool:
		b, err := parseBool(n.Value)
		if err != nil {
			return "", err
		}
		switch n.Op {
		case "=":
			return "(coalesce(" + f.expr + ", false) = " + c.arg(b) + ")", nil
		case "!=":
			return "(coalesce(" + f.expr + ", false) <> " + c.arg(b) + ")", nil
		}
		return "", bad()
	}
	if n.Op == "~" && f.kind != kString && f.kind != kVerdict {
		return "", bad()
	}
	v, err := c.literal(f, n.Value)
	if err != nil {
		return "", err
	}
	switch n.Op {
	case "=", "<", "<=", ">", ">=":
		return "(" + f.expr + " " + n.Op + " " + c.arg(v) + ")", nil
	case "!=":
		return "(" + f.expr + " IS DISTINCT FROM " + c.arg(v) + ")", nil
	case "~":
		return "(" + f.expr + " GLOB " + c.arg(v) + ")", nil
	}
	return "", bad()
}

// in compiles field in [values].
func (c *compiler) in(n In) (string, error) {
	f, err := fieldAt(n.Field, n.Pos)
	if err != nil {
		return "", err
	}
	if f.kind == kBool {
		return "", &SyntaxError{n.Pos, fmt.Sprintf("\"in\" does not apply to %s", f.Name)}
	}
	phs := make([]string, len(n.Values))
	for i, v := range n.Values {
		lit, err := c.literal(f, v)
		if err != nil {
			return "", err
		}
		phs[i] = c.arg(lit)
	}
	if f.kind == kList || f.kind == kRef {
		return "list_has_any(" + f.expr + ", [" + strings.Join(phs, ", ") + "])", nil
	}
	if f.kind == kFinding {
		return findingIn(f.expr + " IN (" + strings.Join(phs, ", ") + ")"), nil
	}
	return "(" + f.expr + " IN (" + strings.Join(phs, ", ") + "))", nil
}

// exists compiles field exists.
func (c *compiler) exists(n Exists) (string, error) {
	f, err := fieldAt(n.Field, n.Pos)
	if err != nil {
		return "", err
	}
	switch f.kind {
	case kList:
		return "(len(coalesce(" + f.expr + ", [])) > 0)", nil
	case kRef:
		return "(" + f.present + ")", nil
	case kFinding:
		return findingIn("true"), nil
	}
	return "(" + f.expr + " IS NOT NULL)", nil
}

// findingIn matches images with a stored finding meeting cond. Findings
// kept from an older scan, whose counts have since changed, are left out.
func findingIn(cond string) string {
	return "(digest IN (SELECT v.digest FROM vulnerabilities v SEMI JOIN scans s ON s.digest = v.digest AND s.findings WHERE " + cond + "))"
}

// flag compiles a field used as a condition on its own.
func (c *compiler) flag(n Flag) (string, error) {
	f, err := fieldAt(n.Field, n.Pos)
	if err != nil {
		return "", err
	}
	switch f.kind {
	case kBool:
		return "coalesce(" + f.expr + ", false)", nil
	case kInt, kBytes, kDays:
		return "(coalesce(" + f.expr + ", 0) > 0)", nil
	case kVerdict:
		return "(" + f.expr + " IN ('drifting', 'stale', 'abandoned'))", nil
	case kString:
		return "(coalesce(lower(" + f.expr + "), '') NOT IN ('', 'false', '0', 'no'))", nil
	}
	return c.exists(Exists(n))
}

// call compiles a function.
func (c *compiler) call(n Call) (string, error) {
	switch strings.ToLower(n.Name) {
	case "ancestor":
		if len(n.Args) != 1 {
			return "", &SyntaxError{n.Pos, "ancestor takes one ref"}
		}
		ref := n.Args[0].Text
		return "(digest IN (WITH RECURSIVE d(id) AS (" +
			"SELECT digest FROM resolve_ref(" + c.arg(ref) + ") " +
			"UNION SELECT l.digest FROM lineage l JOIN d ON l.parent = d.id) " +
			"SELECT id FROM d WHERE id NOT IN (SELECT digest FROM resolve_ref(" + c.arg(ref) + "))))", nil
	}
	return "", &SyntaxError{n.Pos, fmt.Sprintf("unknown function %q (available: ancestor)", n.Name)}
}

// literal converts a value to the Go type the field's column compares
// against.
func (c *compiler) literal(f Field, v Value) (any, error) {
	switch f.kind {
	case kInt:
		n, err := strconv.ParseInt(v.Text, 10, 64)
		if err != nil {
			return nil, &SyntaxError{v.Pos, fmt.Sprintf("%s wants a whole number, got %s", f.Name, v)}
		}
		return n, nil
	case kBytes:
		n, err := ParseBytes(v.Text)
		if err != nil {
			return nil, &SyntaxError{v.Pos, err.Error()}
		}
		return n, nil
	case kDays:
		d, err := ParseDays(v.Text)
		if err != nil {
			return nil, &SyntaxError{v.Pos, err.Error()}
		}
		return d, nil
	case kTime:
		t, err := parseTime(v.Text)
		if err != nil {
			return nil, &SyntaxError{v.Pos, err.Error()}
		}
		return t, nil
	case kVerdict:
		switch v.Text {
		case "fresh", "drifting", "stale", "abandoned":
			return v.Text, nil
		}
		if strings.ContainsAny(v.Text, "*?[") {
			return v.Text, nil
		}
		return nil, &SyntaxError{v.Pos, fmt.Sprintf("unknown verdict %s (fresh, drifting, stale, abandoned)", v)}
	}
	return v.Text, nil
}

// parseBool parses true or false.
func parseBool(v Value) (bool, error) {
	switch strings.ToLower(v.Text) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, &SyntaxError{v.Pos, fmt.Sprintf("expected true or false, got %s", v)}
}

// parseTime parses a date or RFC 3339 time.
func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("expected a date such as 2026-01-31, got %q", s)
}

// splitUnit splits a number from its unit suffix.
func splitUnit(s string) (float64, string, error) {
	i := len(s)
	for i > 0 && (s[i-1] < '0' || s[i-1] > '9') && s[i-1] != '.' {
		i--
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, "", fmt.Errorf("%q is not a number", s)
	}
	return n, strings.ToLower(s[i:]), nil
}

// ParseDays parses a duration as days: 180d, 12h, 6w, 3mo, 1y, or a plain
// number of days.
func ParseDays(s string) (float64, error) {
	n, unit, err := splitUnit(s)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{"": 1, "d": 1, "h": 1.0 / 24, "w": 7, "mo": 30, "y": 365}
	m, ok := mult[unit]
	if !ok {
		return 0, fmt.Errorf("unknown duration unit in %q (use h, d, w, mo, y)", s)
	}
	return n * m, nil
}

// ParseBytes parses a size: 512, 10KB, 1.5GiB.
func ParseBytes(s string) (int64, error) {
	n, unit, err := splitUnit(s)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{
		"": 1, "b": 1,
		"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	}
	m, ok := mult[unit]
	if !ok {
		return 0, fmt.Errorf("unknown size unit in %q (use B, KB, MB, GB, TB or KiB, MiB, GiB, TiB)", s)
	}
	v := n * m
	if v > math.MaxInt64 || v < math.MinInt64 {
		return 0, fmt.Errorf("size %q is out of range", s)
	}
	return int64(v), nil
}

// quote returns s as a SQL string literal.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
