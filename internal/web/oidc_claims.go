package web

import (
	"context"

	"golang.org/x/oauth2"
)

// Who logs in with OpenID Connect: the claims of the ID token, and the groups from the user info endpoint when the
// token has none.

// oidcClaims are the claims the relay reads (identity providers usually put the groups into the profile scope).
type oidcClaims struct {
	Subject  string   `json:"sub"`
	Username string   `json:"preferred_username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
	Nonce    string   `json:"nonce"`
}

// oidcIdentity redeems the authorization code and returns the claims of the verified ID token. On failure msg is
// the text for the user and err (may be nil) the detail for the log.
func (s *Server) oidcIdentity(ctx context.Context, code string, st oidcState) (cl oidcClaims, msg string, err error) {
	tok, err := s.oidc.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.verifier))
	if err != nil {
		return cl, msgOIDCToken, err
	}
	if cl, msg, err = s.oidcIDTokenClaims(ctx, tok, st.nonce); msg != "" {
		return cl, msg, err
	}
	if len(cl.Groups) == 0 {
		cl.Groups = s.oidcUserInfoGroups(ctx, tok)
	}
	return cl, "", nil
}

// oidcIDTokenClaims verifies the ID token that came with tok and returns its claims; the token must carry nonce.
// On failure msg is the text for the user and err (may be nil) the detail for the log.
func (s *Server) oidcIDTokenClaims(ctx context.Context, tok *oauth2.Token, nonce string) (cl oidcClaims, msg string, err error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return cl, msgOIDCNoToken, nil
	}
	idt, err := s.oidc.verifier.Verify(ctx, raw)
	if err != nil {
		return cl, msgOIDCBad, err
	}
	if err = idt.Claims(&cl); err != nil || cl.Nonce != nonce {
		return cl, msgOIDCNonce, err
	}
	return cl, "", nil
}

// oidcUserInfoGroups reads the groups from the userinfo endpoint, for setups that put them only there (nil when
// that fails).
func (s *Server) oidcUserInfoGroups(ctx context.Context, tok *oauth2.Token) []string {
	ui, err := s.oidc.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		return nil
	}
	var more oidcClaims
	if ui.Claims(&more) != nil {
		return nil
	}
	return more.Groups
}
