package auth

import (
	"encoding/json"
	"strings"

	"bkt/internal/database"
	"bkt/internal/models"

	"gorm.io/gorm"
)

// Bucket policies name principals by *username* (see security.matchesPrincipal).
// Usernames become free again when a user is deleted, so a stale principal
// would silently grant the old user's bucket access to whoever next receives
// that name. These helpers keep the two in sync:
//   - RemovePrincipalFromBucketPolicies scrubs a deleted user's name, and
//   - UsernameReferencedByBucketPolicy lets provisioning avoid names that are
//     still referenced (e.g. by policies written before the scrub existed).

// UsernameReferencedByBucketPolicy reports whether any bucket policy names
// username as a principal. Errors are treated as "referenced" (fail closed).
func UsernameReferencedByBucketPolicy(username string) bool {
	return usernameReferencedByBucketPolicyTx(database.DB, username)
}

func usernameReferencedByBucketPolicyTx(tx *gorm.DB, username string) bool {
	if tx == nil || username == "" {
		return false
	}
	var docs []string
	if err := tx.Model(&models.BucketPolicy{}).
		Where("policy_document::text LIKE ?", "%"+likeEscape(jsonQuoted(username))+"%").
		Pluck("policy_document", &docs).Error; err != nil {
		return true
	}
	for _, d := range docs {
		if policyNamesPrincipal(d, username) {
			return true
		}
	}
	return false
}

// RemovePrincipalFromBucketPolicies removes username from the Principal of
// every bucket-policy statement. A statement whose Principal named only that
// user is dropped entirely (removing Principal would widen it to everyone);
// a policy left without statements is deleted. Returns the number of bucket
// policies changed.
func RemovePrincipalFromBucketPolicies(tx *gorm.DB, username string) (int, error) {
	var rows []models.BucketPolicy
	if err := tx.Where("policy_document::text LIKE ?", "%"+likeEscape(jsonQuoted(username))+"%").
		Find(&rows).Error; err != nil {
		return 0, err
	}
	changed := 0
	for _, bp := range rows {
		doc, modified, empty, err := scrubPrincipal(bp.PolicyDocument, username)
		if err != nil || !modified {
			continue
		}
		if empty {
			if err := tx.Where("bucket_id = ?", bp.BucketID).Delete(&models.BucketPolicy{}).Error; err != nil {
				return changed, err
			}
		} else if err := tx.Model(&models.BucketPolicy{}).Where("bucket_id = ?", bp.BucketID).
			Update("policy_document", doc).Error; err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// policyNamesPrincipal reports whether a policy document lists username in any
// statement's Principal.
func policyNamesPrincipal(doc, username string) bool {
	_, modified, _, err := scrubPrincipal(doc, username)
	return err == nil && modified
}

// scrubPrincipal returns doc with username removed from all Principal values.
// modified reports whether anything changed; empty reports that no statement
// remains.
func scrubPrincipal(doc, username string) (out string, modified, empty bool, err error) {
	var policy map[string]interface{}
	if err = json.Unmarshal([]byte(doc), &policy); err != nil {
		return doc, false, false, err
	}
	var stmts []interface{}
	single := false
	switch s := policy["Statement"].(type) {
	case []interface{}:
		stmts = s
	case map[string]interface{}:
		stmts = []interface{}{s}
		single = true
	default:
		return doc, false, false, nil
	}

	kept := make([]interface{}, 0, len(stmts))
	for _, raw := range stmts {
		st, ok := raw.(map[string]interface{})
		if !ok {
			kept = append(kept, raw)
			continue
		}
		p, has := st["Principal"]
		if !has || p == nil {
			kept = append(kept, st)
			continue
		}
		np, changed, nowEmpty := removeFromPrincipal(p, username)
		if !changed {
			kept = append(kept, st)
			continue
		}
		modified = true
		if nowEmpty {
			continue // drop: an absent Principal would apply to everyone
		}
		st["Principal"] = np
		kept = append(kept, st)
	}
	if !modified {
		return doc, false, false, nil
	}
	if len(kept) == 0 {
		return "", true, true, nil
	}
	if single && len(kept) == 1 {
		policy["Statement"] = kept[0]
	} else {
		policy["Statement"] = kept
	}
	b, err := json.Marshal(policy)
	if err != nil {
		return doc, false, false, err
	}
	return string(b), true, false, nil
}

// removeFromPrincipal handles the Principal shapes: "name", ["a","b"], and a
// map of such values ({"AWS": [...]}).
func removeFromPrincipal(p interface{}, username string) (out interface{}, changed, empty bool) {
	switch v := p.(type) {
	case string:
		if v == username {
			return nil, true, true
		}
		return v, false, false
	case []interface{}:
		kept := make([]interface{}, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok && s == username {
				changed = true
				continue
			}
			kept = append(kept, x)
		}
		return kept, changed, changed && len(kept) == 0
	case map[string]interface{}:
		nm := make(map[string]interface{}, len(v))
		for k, x := range v {
			nx, c, e := removeFromPrincipal(x, username)
			if c {
				changed = true
			}
			if !e {
				nm[k] = nx
			}
		}
		return nm, changed, changed && len(nm) == 0
	}
	return p, false, false
}

func jsonQuoted(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// likeEscape escapes LIKE wildcards (PostgreSQL's default escape is '\').
func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
