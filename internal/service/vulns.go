package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/rossedman/strata/internal/query"
)

// DefaultInUseDays is how recently an image must have been pulled to
// count as in use, when the caller does not say.
const DefaultInUseDays = 30

// VulnBase is a base image whose scan found vulnerabilities, and what is
// built on it.
type VulnBase struct {
	Digest   string `json:"digest"`
	Ref      string `json:"ref"`
	Repo     string `json:"repo"`
	Critical int    `json:"critical"`
	High     int    `json:"high"`
	// Fixable is set when the base's findings were fetched.
	Fixable *int `json:"fixable,omitempty"`
	// Images counts images built on the base at any depth; InUse counts
	// those pulled within the in-use window; Vulnerable counts those
	// whose own scan found vulnerabilities of the severity ranked by.
	Images     int `json:"images"`
	InUse      int `json:"in_use"`
	Vulnerable int `json:"vulnerable"`
	// Groups counts the distinct values of the group-by field (teams, by
	// default) among the images built on it.
	Groups int `json:"groups"`
}

// VulnBasesOptions shape the ranking of vulnerable bases.
type VulnBasesOptions struct {
	// Severity ranks by critical (the default) or high findings.
	Severity string
	// GroupBy is the field whose values Groups counts; empty means
	// label.team.
	GroupBy string
	// InUseDays is the pull window for InUse; zero means 30.
	InUseDays int
	Limit     int
}

// vulnColumn returns the image_view column counting a severity.
func vulnColumn(severity string) (string, error) {
	switch severity {
	case "", "critical":
		return "vuln_critical", nil
	case "high":
		return "vuln_high", nil
	}
	return "", input(fmt.Errorf("severity %q: use critical or high", severity))
}

// VulnBases ranks the bases that carry the most vulnerabilities to the
// most images: those with findings of the severity and images built on
// them, ordered by findings times images in use, then times images.
func (s *Service) VulnBases(ctx context.Context, opts VulnBasesOptions) ([]VulnBase, error) {
	col, err := vulnColumn(opts.Severity)
	if err != nil {
		return nil, err
	}
	g, err := groupField(opts.GroupBy)
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	inUse := opts.InUseDays
	if inUse <= 0 {
		inUse = DefaultInUseDays
	}
	v := s.view()
	// Candidates are cut to a multiple of the limit by findings times
	// descendants before walking their subtrees.
	rows, err := s.db().QueryContext(ctx, `
		WITH RECURSIVE bases AS (
			SELECT digest FROM `+v+` WHERE descendants > 0 AND `+col+` > 0
			ORDER BY `+col+` * descendants DESC, digest LIMIT ?
		), sub(base, id) AS (
			SELECT digest, digest FROM bases
			UNION SELECT s.base, l.digest FROM lineage l JOIN sub s ON l.parent = s.id
		), agg AS (
			SELECT s.base, count(*) AS images,
				count(*) FILTER (WHERE i.stale_last_pulled_days <= ?) AS in_use,
				count(*) FILTER (WHERE i.`+col+` > 0) AS vulnerable,
				count(DISTINCT `+g+`) AS groups
			FROM sub s JOIN `+v+` i ON i.digest = s.id WHERE s.id <> s.base GROUP BY s.base
		)
		SELECT b.digest, coalesce(b.tag_refs[1], b.repo || '@' || b.digest), b.repo,
			coalesce(b.vuln_critical, 0), coalesce(b.vuln_high, 0), b.vuln_fixable,
			a.images, a.in_use, a.vulnerable, a.groups
		FROM agg a JOIN `+v+` b ON b.digest = a.base
		ORDER BY b.`+col+` * a.in_use DESC, b.`+col+` * a.images DESC, b.digest
		LIMIT ?`, max(10*limit, 200), inUse, limit)
	if err != nil {
		return nil, fmt.Errorf("rank vulnerable bases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []VulnBase{}
	for rows.Next() {
		var b VulnBase
		var fixable sql.NullInt64
		if err := rows.Scan(&b.Digest, &b.Ref, &b.Repo, &b.Critical, &b.High, &fixable, &b.Images, &b.InUse, &b.Vulnerable, &b.Groups); err != nil {
			return nil, err
		}
		if fixable.Valid {
			n := int(fixable.Int64)
			b.Fixable = &n
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// groupField returns the SQL for a group-by field, label.team by default.
func groupField(field string) (string, error) {
	if field == "" {
		field = "label.team"
	}
	g, err := groupExpr(field)
	if err != nil {
		return "", input(err)
	}
	return g, nil
}

// ImpactOptions pick what to measure the blast radius of: a base image
// or a vulnerability.
type ImpactOptions struct {
	// Ref is a base: every image built on it is affected.
	Ref string
	// CVE is a vulnerability id: images whose scan found it are
	// affected, and so are unscanned images built on one of them.
	CVE string
	// Filter narrows the affected images, such as
	// stale.last_pulled < 30d.
	Filter string
	// GroupBy is the field images are grouped by; empty means
	// label.team.
	GroupBy string
	// InUseDays is the pull window for InUse; zero means 30.
	InUseDays int
}

// Impact is the blast radius of a base or a vulnerability.
type Impact struct {
	// Target is the base ref or vulnerability id asked about.
	Target string `json:"target"`
	// Origins are where the vulnerability enters: the base itself, or
	// for a CVE the images that have it while their parent does not.
	Origins []ImpactOrigin `json:"origins"`
	// Groups total the affected images per value of the group-by field,
	// most affected first.
	GroupBy string        `json:"group_by"`
	Groups  []ImpactGroup `json:"groups"`
	// Images are the affected images, in use first.
	Images []ImpactImage `json:"images"`
	Total  int           `json:"total"`
	InUse  int           `json:"in_use"`
}

// ImpactOrigin is an image a vulnerability enters through.
type ImpactOrigin struct {
	Digest string `json:"digest"`
	Ref    string `json:"ref"`
	// Images counts affected images that inherit from it, itself
	// included when it is affected.
	Images int `json:"images"`
}

// ImpactGroup totals the affected images with one group-by value.
type ImpactGroup struct {
	Value  string `json:"value"`
	Images int    `json:"images"`
	InUse  int    `json:"in_use"`
	Repos  int    `json:"repos"`
}

// ImpactImage is one affected image.
type ImpactImage struct {
	Digest string `json:"digest"`
	Ref    string `json:"ref"`
	Repo   string `json:"repo"`
	Group  string `json:"group"`
	// Critical and High are the image's own scan counts, unset when it
	// was not scanned.
	Critical *int   `json:"critical"`
	High     *int   `json:"high"`
	Stale    string `json:"stale"`
	// LastPulledDays is unset when the registry reports no pulls.
	LastPulledDays *int `json:"last_pulled_days"`
	InUse          bool `json:"in_use"`
	// Via is the origin the image is affected through.
	Via string `json:"via"`
	// Evidence is "scan" when the image's own scan found the
	// vulnerability, "built on" for an image built on the base asked
	// about, and "inherited" for an unscanned image built on an affected
	// one.
	Evidence string `json:"evidence"`
}

// ImpactColumns are the columns of an impact export, in order.
var ImpactColumns = []string{"digest", "ref", "repo", "group", "critical", "high", "stale", "last_pulled_days", "in_use", "via", "evidence"}

// Row returns the image as values in ImpactColumns order.
func (i ImpactImage) Row() []any {
	return []any{i.Digest, i.Ref, i.Repo, i.Group, intOrNil(i.Critical), intOrNil(i.High), i.Stale, intOrNil(i.LastPulledDays), i.InUse, i.Via, i.Evidence}
}

// intOrNil returns *p, or nil.
func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// validFindings selects the digests with stored findings that are still
// current.
const validFindings = `SELECT v.digest FROM vulnerabilities v SEMI JOIN scans s ON s.digest = v.digest AND s.findings`

// Impact finds everything a base or a vulnerability reaches, grouped by
// team (or another field), with the images that bring it in.
func (s *Service) Impact(ctx context.Context, opts ImpactOptions) (*Impact, error) {
	if (opts.Ref == "") == (opts.CVE == "") {
		return nil, input(fmt.Errorf("name a base ref or a CVE, not both"))
	}
	g, err := groupField(opts.GroupBy)
	if err != nil {
		return nil, err
	}
	c, err := query.Compile(opts.Filter)
	if err != nil {
		return nil, input(err)
	}
	inUse := opts.InUseDays
	if inUse <= 0 {
		inUse = DefaultInUseDays
	}
	out := &Impact{Target: opts.Ref + opts.CVE, GroupBy: opts.GroupBy, Origins: []ImpactOrigin{}, Groups: []ImpactGroup{}, Images: []ImpactImage{}}
	if out.GroupBy == "" {
		out.GroupBy = "label.team"
	}
	// reach(origin, id, evidence) holds every affected image with the
	// origin it is reached from.
	var reach string
	var args []any
	if opts.Ref != "" {
		ids, err := s.resolveAll(ctx, opts.Ref)
		if err != nil {
			return nil, err
		}
		reach = `reach(origin, id) AS (
				SELECT unnest(?::VARCHAR[]), unnest(?::VARCHAR[])
				UNION SELECT r.origin, l.digest FROM lineage l JOIN reach r ON l.parent = r.id
			), affected AS (
				SELECT id, min(origin) AS origin, 'built on' AS evidence FROM reach
				WHERE NOT list_contains(?::VARCHAR[], id) GROUP BY id
			)`
		args = []any{ids, ids, ids}
	} else {
		// The walk stops at images scanned without the vulnerability:
		// they were rebuilt or patched.
		reach = `hits AS (
				SELECT DISTINCT digest FROM (` + validFindings + ` WHERE v.id = ?)
			), known AS (
				SELECT digest FROM scans WHERE findings
			), origins AS (
				SELECT h.digest FROM hits h LEFT JOIN lineage l ON l.digest = h.digest
				WHERE l.parent IS NULL OR l.parent NOT IN (SELECT digest FROM hits)
			), reach(origin, id) AS (
				SELECT digest, digest FROM origins
				UNION SELECT r.origin, l.digest FROM lineage l JOIN reach r ON l.parent = r.id
				WHERE l.digest IN (SELECT digest FROM hits) OR l.digest NOT IN (SELECT digest FROM known)
			), affected AS (
				SELECT id, min(origin) AS origin,
					CASE WHEN id IN (SELECT digest FROM hits) THEN 'scan' ELSE 'inherited' END AS evidence
				FROM reach GROUP BY id
			)`
		args = []any{opts.CVE}
	}
	v := s.view()
	q := `WITH RECURSIVE ` + reach + `
		SELECT i.digest, coalesce(i.tag_refs[1], i.repo || '@' || i.digest), i.repo, coalesce(` + g + `::VARCHAR, ''),
			i.vuln_critical, i.vuln_high, coalesce(i.stale, ''), i.stale_last_pulled_days,
			coalesce(i.stale_last_pulled_days <= ?, false), a.origin, a.evidence,
			coalesce((SELECT coalesce(o.tag_refs[1], o.repo || '@' || o.digest) FROM ` + v + ` o WHERE o.digest = a.origin), a.origin)
		FROM affected a JOIN ` + v + ` i ON i.digest = a.id
		WHERE ` + c.Where + `
		ORDER BY 9 DESC, i.stale_last_pulled_days NULLS LAST, i.repo, i.digest`
	args = append(append(args, inUse), c.Args...)
	rows, err := s.db().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("walk impact: %w", err)
	}
	defer func() { _ = rows.Close() }()
	origins := map[string]*ImpactOrigin{}
	groups := map[string]*ImpactGroup{}
	repos := map[string]map[string]bool{}
	for rows.Next() {
		var im ImpactImage
		var crit, high, pulled sql.NullInt64
		var origin, originRef string
		if err := rows.Scan(&im.Digest, &im.Ref, &im.Repo, &im.Group, &crit, &high, &im.Stale, &pulled, &im.InUse, &origin, &im.Evidence, &originRef); err != nil {
			return nil, err
		}
		im.Critical, im.High, im.LastPulledDays = nullInt(crit), nullInt(high), nullInt(pulled)
		im.Via = originRef
		out.Images = append(out.Images, im)
		if origins[origin] == nil {
			origins[origin] = &ImpactOrigin{Digest: origin, Ref: originRef}
		}
		origins[origin].Images++
		if groups[im.Group] == nil {
			groups[im.Group] = &ImpactGroup{Value: im.Group}
			repos[im.Group] = map[string]bool{}
		}
		gr := groups[im.Group]
		gr.Images++
		if im.InUse {
			gr.InUse++
			out.InUse++
		}
		repos[im.Group][im.Repo] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Total = len(out.Images)
	for k, gr := range groups {
		gr.Repos = len(repos[k])
		out.Groups = append(out.Groups, *gr)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if a.InUse != b.InUse {
			return a.InUse > b.InUse
		}
		if a.Images != b.Images {
			return a.Images > b.Images
		}
		return a.Value < b.Value
	})
	for _, o := range origins {
		out.Origins = append(out.Origins, *o)
	}
	sort.Slice(out.Origins, func(i, j int) bool {
		if out.Origins[i].Images != out.Origins[j].Images {
			return out.Origins[i].Images > out.Origins[j].Images
		}
		return out.Origins[i].Ref < out.Origins[j].Ref
	})
	return out, nil
}

// nullInt returns a pointer to n's value, or nil when it is NULL.
func nullInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// IsVulnID reports whether s looks like a vulnerability id rather than an
// image ref: CVE-, GHSA-, or a distribution advisory such as ALAS2-, RHSA-,
// DSA- or USN-.
func IsVulnID(s string) bool {
	up := strings.ToUpper(s)
	for _, p := range []string{"CVE-", "GHSA-", "ALAS", "RHSA-", "DSA-", "DLA-", "USN-", "GO-", "PYSEC-", "OSV-"} {
		if strings.HasPrefix(up, p) && !strings.ContainsAny(s, "/:@") {
			return true
		}
	}
	return false
}
