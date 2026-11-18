package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// PR is the part of `gh pr view --json number,state,headRefOid,headRefName,baseRefName,mergeable,isDraft,
// statusCheckRollup,url` (pr_state) the gate reads.
type PR struct {
	Number                                      int64
	State, HeadSHA, HeadRef, BaseRef, Mergeable string
	Draft                                       bool
	Rollup                                      Rollup
}

// ParsePR decodes pr_state's JSON. A field Python indexes (number, state, headRefOid, mergeable, isDraft) that is
// missing or has another type is an error (Python raises there); the caller then hands the command to Python.
func ParsePR(b []byte) (PR, error) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return PR{}, fmt.Errorf("gh pr view printed no JSON object: %w", err)
	}
	var pr PR
	get := func(k string, into any) error {
		v, ok := raw[k]
		if !ok {
			return fmt.Errorf("gh pr view has no %s: %w", k, ErrUnsupported)
		}
		if err := json.Unmarshal(v, into); err != nil {
			return fmt.Errorf("gh pr view %s: %w: %w", k, err, ErrUnsupported)
		}
		return nil
	}
	for k, into := range map[string]any{"number": &pr.Number, "state": &pr.State, "headRefOid": &pr.HeadSHA,
		"mergeable": &pr.Mergeable} {
		if err := get(k, into); err != nil {
			return PR{}, err
		}
	}
	// null reads as Python's None: a draft flag of null is falsy, a branch name of null never equals a string
	var draft *bool
	if err := get("isDraft", &draft); err != nil {
		return PR{}, err
	}
	pr.Draft = draft != nil && *draft
	for k, into := range map[string]*string{"headRefName": &pr.HeadRef, "baseRefName": &pr.BaseRef} {
		var p *string
		if v, ok := raw[k]; ok {
			if err := json.Unmarshal(v, &p); err != nil {
				return PR{}, fmt.Errorf("gh pr view %s: %w: %w", k, err, ErrUnsupported)
			}
		}
		if p != nil {
			*into = *p
		}
	}
	if pr.HeadSHA == "" {
		return PR{}, fmt.Errorf("gh pr view has an empty headRefOid: %w", ErrUnsupported)
	}
	if v, ok := raw["statusCheckRollup"]; ok {
		var r []map[string]any
		if err := json.Unmarshal(v, &r); err != nil {
			return PR{}, fmt.Errorf("gh pr view statusCheckRollup: %w: %w", err, ErrUnsupported)
		}
		for _, c := range r {
			if c == nil {
				return PR{}, fmt.Errorf("gh pr view statusCheckRollup entry is null: %w", ErrUnsupported)
			}
		}
		pr.Rollup = r
	}
	return pr, nil
}
