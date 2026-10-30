package crawl

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rossedman/strata/internal/model"
)

// Filter narrows what a crawl fetches. Every check runs before the
// requests it would save: repos before their tags are listed, tags before
// they are resolved, and platforms before their manifests are read.
type Filter struct {
	// IncludeRepos keeps only repos matching one of these globs. Empty
	// keeps every repo.
	IncludeRepos []string
	// ExcludeRepos drops repos matching one of these globs.
	ExcludeRepos []string
	// IncludeTags keeps only tags matching one of these globs. Empty keeps
	// every tag.
	IncludeTags []string
	// ExcludeTags drops tags matching one of these globs.
	ExcludeTags []string
	// Platforms keeps only these platforms, such as linux/amd64 or
	// linux/arm64/v8. A platform without a variant matches every variant.
	// Empty keeps every platform.
	Platforms []string
	// PushedSince skips images pushed longer ago than this. It needs a
	// registry that reports push times, such as ECR; zero keeps every
	// image.
	PushedSince time.Duration
}

// Validate reports whether every glob and platform in the filter parses.
func (f Filter) Validate() error {
	_, err := f.compile()
	return err
}

// matcher is a compiled Filter.
type matcher struct {
	includeRepos, excludeRepos []*regexp.Regexp
	includeTags, excludeTags   []*regexp.Regexp
	platforms                  []model.Platform
	pushedSince                time.Duration
}

// compile checks and compiles a filter.
func (f Filter) compile() (*matcher, error) {
	m := &matcher{pushedSince: f.PushedSince}
	var err error
	for _, set := range []struct {
		globs []string
		out   *[]*regexp.Regexp
	}{
		{f.IncludeRepos, &m.includeRepos}, {f.ExcludeRepos, &m.excludeRepos},
		{f.IncludeTags, &m.includeTags}, {f.ExcludeTags, &m.excludeTags},
	} {
		if *set.out, err = compileGlobs(set.globs); err != nil {
			return nil, err
		}
	}
	for _, s := range f.Platforms {
		p, err := ParsePlatform(s)
		if err != nil {
			return nil, err
		}
		m.platforms = append(m.platforms, p)
	}
	if f.PushedSince < 0 {
		return nil, fmt.Errorf("pushed-since must not be negative")
	}
	return m, nil
}

// repo reports whether a repo, named without the registry host, is kept.
func (m *matcher) repo(name string) bool {
	return keep(name, m.includeRepos, m.excludeRepos)
}

// tag reports whether a tag is kept.
func (m *matcher) tag(name string) bool {
	return keep(name, m.includeTags, m.excludeTags)
}

// platform reports whether a platform is kept.
func (m *matcher) platform(p model.Platform) bool {
	if len(m.platforms) == 0 {
		return true
	}
	for _, want := range m.platforms {
		if want.OS == p.OS && want.Arch == p.Arch && (want.Variant == "" || want.Variant == p.Variant) {
			return true
		}
	}
	return false
}

// pushed reports whether an image pushed at t is recent enough. Images
// without a push time are kept.
func (m *matcher) pushed(t, now time.Time) bool {
	return m.pushedSince == 0 || t.IsZero() || !t.Before(now.Add(-m.pushedSince))
}

// keep applies include globs, then exclude globs.
func keep(s string, include, exclude []*regexp.Regexp) bool {
	if len(include) > 0 && !anyMatch(s, include) {
		return false
	}
	return !anyMatch(s, exclude)
}

// anyMatch reports whether any pattern matches s.
func anyMatch(s string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// compileGlobs compiles each glob.
func compileGlobs(globs []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(globs))
	for _, g := range globs {
		re, err := Glob(g)
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

// Glob compiles a repo or tag glob to an anchored regular expression. "*"
// and "?" stay within one path segment, "**" crosses segments, and
// "[...]" is a character class.
func Glob(g string) (*regexp.Regexp, error) {
	if g == "" {
		return nil, fmt.Errorf("empty glob")
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				// "**/" also matches no segments at all, so a/**/b matches a/b.
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(g[i+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("glob %q: unterminated [", g)
			}
			class := g[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		default:
			// Byte by byte, so multi-byte characters reassemble intact.
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", g, err)
	}
	return re, nil
}

// ParsePlatform parses os/arch[/variant].
func ParsePlatform(s string) (model.Platform, error) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return model.Platform{}, fmt.Errorf("platform %q: want os/arch or os/arch/variant", s)
	}
	p := model.Platform{OS: parts[0], Arch: parts[1]}
	if len(parts) == 3 {
		p.Variant = parts[2]
	}
	return p, nil
}
