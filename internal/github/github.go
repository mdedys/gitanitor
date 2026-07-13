package github

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdedys/gitanitor/internal/exec"
)

// Repo identifies the GitHub repository gh will query.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// PRState is the lifecycle state of a pull request.
type PRState string

const (
	Open   PRState = "OPEN"
	Merged PRState = "MERGED"
	Closed PRState = "CLOSED"
)

// PR is a single pull request record as returned by the GraphQL query.
type PR struct {
	Number int
	State  PRState
	URL    string
	Owner  string
}

// Error carries gh's stderr so callers can relay it verbatim.
type Error struct {
	Stderr string
}

func (e *Error) Error() string { return strings.TrimSpace(e.Stderr) }

// ResolveRepo asks gh which repository it will query. gh silently prefers the
// remote named upstream over origin when no default is set, so the caller
// surfaces the result before anything is deleted.
func ResolveRepo(r exec.Runner) (Repo, error) {
	res := r.Run("gh", "repo", "view", "--json", "owner,name")
	if res.ExitCode != 0 {
		return Repo{}, &Error{Stderr: res.Stderr}
	}

	var parsed struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		return Repo{}, &Error{Stderr: fmt.Sprintf("could not parse gh repo view output: %v", err)}
	}
	return Repo{Owner: parsed.Owner.Login, Name: parsed.Name}, nil
}

// LookupPRs runs one batched GraphQL query covering every branch and returns
// the PRs found per branch. Branch names are passed as GraphQL variables,
// never interpolated into the query. An unknown branch maps to an empty slice.
func LookupPRs(r exec.Runner, repo Repo, branches []string) (map[string][]PR, error) {
	if len(branches) == 0 {
		return map[string][]PR{}, nil
	}

	query, aliases := buildQuery(repo, branches)

	args := []string{"api", "graphql", "-f", "query=" + query}
	for alias, branch := range aliases {
		args = append(args, "-f", alias+"="+branch)
	}

	res := r.Run("gh", args...)
	if res.ExitCode != 0 {
		return nil, &Error{Stderr: res.Stderr}
	}

	return parseResponse(res.Stdout, aliases)
}

// buildQuery assembles the GraphQL document and the alias→branch map. Aliases
// are generated identifiers (b0, b1, ...) so branch names never touch the query
// text — they arrive as typed variables.
func buildQuery(repo Repo, branches []string) (string, map[string]string) {
	aliases := make(map[string]string, len(branches))
	// Sorted alias order keeps the query deterministic for tests.
	var params []string
	var fields []string
	for i, branch := range branches {
		alias := fmt.Sprintf("b%d", i)
		aliases[alias] = branch
		params = append(params, fmt.Sprintf("$%s: String!", alias))
		fields = append(fields, fmt.Sprintf(
			"    %s: pullRequests(headRefName: $%s, first: 20) {\n"+
				"      nodes { number state mergedAt url headRepositoryOwner { login } }\n"+
				"    }",
			alias, alias))
	}

	query := fmt.Sprintf(
		"query(%s) {\n"+
			"  repository(owner: %q, name: %q) {\n"+
			"%s\n"+
			"  }\n"+
			"}",
		strings.Join(params, ", "), repo.Owner, repo.Name, strings.Join(fields, "\n"))

	return query, aliases
}

func parseResponse(stdout string, aliases map[string]string) (map[string][]PR, error) {
	var parsed struct {
		Data struct {
			Repository map[string]struct {
				Nodes []struct {
					Number              int     `json:"number"`
					State               PRState `json:"state"`
					URL                 string  `json:"url"`
					HeadRepositoryOwner *struct {
						Login string `json:"login"`
					} `json:"headRepositoryOwner"`
				} `json:"nodes"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return nil, &Error{Stderr: fmt.Sprintf("could not parse gh graphql output: %v", err)}
	}

	out := make(map[string][]PR, len(aliases))
	for alias, branch := range aliases {
		repoField := parsed.Data.Repository[alias]
		prs := make([]PR, 0, len(repoField.Nodes))
		for _, n := range repoField.Nodes {
			owner := ""
			if n.HeadRepositoryOwner != nil {
				owner = n.HeadRepositoryOwner.Login
			}
			prs = append(prs, PR{Number: n.Number, State: n.State, URL: n.URL, Owner: owner})
		}
		out[branch] = prs
	}
	return out, nil
}
