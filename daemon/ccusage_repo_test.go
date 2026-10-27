package main

import (
	"context"
	"testing"
)

// TestNormalizeGitRemote canonicalizes every git-remote form git emits (scp-like,
// ssh://, https:// with/without creds, nested GitLab groups, host case) to a clean
// "https://<host>/<owner>/<repo>" — and yields "" for anything unparseable.
func TestNormalizeGitRemote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "scp-like github",
			in:   "git@github.com:example-org/soluterminal.git",
			want: "https://github.com/example-org/soluterminal",
		},
		{
			name: "https with .git suffix",
			in:   "https://github.com/owner/repo.git",
			want: "https://github.com/owner/repo",
		},
		{
			name: "https without .git suffix",
			in:   "https://github.com/owner/repo",
			want: "https://github.com/owner/repo",
		},
		{
			name: "ssh url with port and .git",
			in:   "ssh://git@github.com:22/owner/repo.git",
			want: "https://github.com/owner/repo",
		},
		{
			name: "https with embedded credentials (stripped)",
			in:   "https://user:ghp_token@github.com/owner/repo.git",
			want: "https://github.com/owner/repo",
		},
		{
			name: "git protocol url",
			in:   "git://github.com/owner/repo.git",
			want: "https://github.com/owner/repo",
		},
		{
			name: "nested gitlab group path preserved",
			in:   "git@gitlab.com:group/subgroup/repo.git",
			want: "https://gitlab.com/group/subgroup/repo",
		},
		{
			name: "host lowercased, path case preserved",
			in:   "git@GitHub.com:Owner/Repo.git",
			want: "https://github.com/Owner/Repo",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "not a url (no scheme, no colon)",
			in:   "not a url",
			want: "",
		},
		{
			name: "trailing slash trimmed",
			in:   "https://github.com/owner/repo/",
			want: "https://github.com/owner/repo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeGitRemote(tc.in); got != tc.want {
				t.Errorf("normalizeGitRemote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRepoResolverEmptyCwd asserts an empty / whitespace-only cwd short-circuits
// to "" WITHOUT exec'ing git (hermetic: no real repo on disk required).
func TestRepoResolverEmptyCwd(t *testing.T) {
	t.Parallel()
	rr := newRepoResolver()
	if got := rr.resolve(context.Background(), ""); got != "" {
		t.Errorf(`resolve(ctx, "") = %q, want ""`, got)
	}
	if got := rr.resolve(context.Background(), "   "); got != "" {
		t.Errorf(`resolve(ctx, "   ") = %q, want ""`, got)
	}
}
