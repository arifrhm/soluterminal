import { describe, expect, it } from 'vitest';
import { SIGNIN_EMAIL_MAY_CONTAIN_LINK } from './authEmailCopy';

describe('sign-in email copy switch', () => {
  // The auth email router sends SoluTerminal's emails with a code and no
  // link, so the code step no longer says "you can ignore any link".
  it('drops the ignore-the-link hint now the email router is live', () => {
    expect(SIGNIN_EMAIL_MAY_CONTAIN_LINK).toBe(false);
  });
});
