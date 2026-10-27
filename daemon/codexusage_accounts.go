package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
)

// Codex usage account labels. A rollout's session_meta names the ChatGPT
// account and user that created it (creator_account_id / creator_user_id).
// Those ids also live in each saved login's id_token claims, so the usage
// scanner can label a rollout with the saved login's name — the same label the
// Credentials UI shows. A ChatGPT workspace account id can be shared by several
// users, so both ids must match.

// codexAuthUsageIdentity extracts (account id, user id, email) from an
// auth.json body without exposing any token. Missing pieces are "".
func codexAuthUsageIdentity(authJSON []byte) (accountID, userID, email string) {
	var a struct {
		Tokens struct {
			IDToken   string `json:"id_token"`
			Access    string `json:"access_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(authJSON, &a) != nil {
		return "", "", ""
	}
	accountID = a.Tokens.AccountID
	for _, token := range []string{a.Tokens.IDToken, a.Tokens.Access} {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		b, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims struct {
			Email string `json:"email"`
			Auth  struct {
				AccountID string `json:"chatgpt_account_id"`
				UserID    string `json:"chatgpt_user_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if json.Unmarshal(b, &claims) != nil {
			continue
		}
		if accountID == "" {
			accountID = claims.Auth.AccountID
		}
		if userID == "" {
			userID = claims.Auth.UserID
		}
		if email == "" {
			email = strings.ToLower(strings.TrimSpace(claims.Email))
		}
	}
	return accountID, userID, email
}

func codexUsageIdentityKey(accountID, userID string) string {
	return accountID + "\x00" + userID
}

// codexUsageResolver completes a saved-login index (identity key → label)
// with the live auth.json's email, and returns the resolver from a rollout's
// creator ids to a label.
func codexUsageResolver(index map[string]string, livePath string) codexAccountResolver {
	if raw, err := os.ReadFile(livePath); err == nil {
		if acct, user, email := codexAuthUsageIdentity(raw); acct != "" && user != "" && email != "" {
			if _, taken := index[codexUsageIdentityKey(acct, user)]; !taken {
				index[codexUsageIdentityKey(acct, user)] = email
			}
		}
	}
	return func(accountID, userID string) string {
		if accountID == "" || userID == "" {
			return ""
		}
		return index[codexUsageIdentityKey(accountID, userID)]
	}
}

// UsageAccountResolver labels usage with the live login's email.
func (l *codexCLILogin) UsageAccountResolver() codexAccountResolver {
	if l == nil {
		return nil
	}
	return codexUsageResolver(map[string]string{}, l.livePath())
}
