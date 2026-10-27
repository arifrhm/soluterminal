// Sign-in email wording that depends on how the shared Supabase project sends
// its emails.
//
// Before the auth email router, some accounts got Supabase's stock "Magic
// Link" email, with a link as well as (or instead of) the code, so the code
// step told people to ignore the link. Since 2026-10-23 the router sends every
// email started from SoluTerminal (it sends a SoluTerminal redirect, see
// authEmailRedirect in cloud.ts) with the code and no link. Set this back to
// true if the router is ever switched off.
export const SIGNIN_EMAIL_MAY_CONTAIN_LINK = false;
