import type { Session } from '@supabase/supabase-js';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { askToConnectAfterSignIn, connectThisComputer, CONNECT_PROMPT_BODY, CONNECT_PROMPT_TITLE } from './accountConnect';
import { useStore } from './store';
import { useCloud } from './cloud';

afterEach(() => {
  vi.restoreAllMocks();
  useCloud.setState({ session: null, isLocal: false });
});

describe('connectThisComputer', () => {
  it('needs a signed-in local session', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch');
    useCloud.setState({ session: null, isLocal: true });
    expect((await connectThisComputer()).status).toBe('signin');
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('sends the account token to the daemon and returns its answer', async () => {
    useCloud.setState({ isLocal: true, session: { access_token: 'acct', user: { id: 'u1' } } as unknown as Session });
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ status: 'machine_limit', message: 'up to 3 computers' }), { status: 409 }),
    );
    const res = await connectThisComputer({ force: true });
    expect(res).toEqual({ status: 'machine_limit', message: 'up to 3 computers' });
    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/cloud/pair/account');
    expect((init.headers as Record<string, string>)['X-Solu-Account-Token']).toBe('acct');
    expect(JSON.parse(String(init.body))).toEqual({ force: true });
  });

  it('never sends an anonymous session', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch');
    useCloud.setState({ isLocal: true, session: { access_token: 'anon', user: { id: 'u2', is_anonymous: true } } as unknown as Session });
    expect((await connectThisComputer()).status).toBe('signin');
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});

describe('askToConnectAfterSignIn', () => {
  function signedIn() {
    useCloud.setState({ isLocal: true, session: { access_token: 'acct', user: { id: 'u3' } } as unknown as Session });
  }
  function daemon(answers: Array<{ status: string }>) {
    const bodies: Array<Record<string, boolean>> = [];
    const spy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (_url, init) => {
      bodies.push(JSON.parse(String((init as RequestInit).body)));
      const next = answers.shift() ?? { status: 'error' };
      return new Response(JSON.stringify(next), { status: 200 });
    });
    return { spy, bodies };
  }
  function answerDialog(choice: boolean) {
    const opened: Array<{ title?: string; message: string; confirmLabel?: string; cancelLabel?: string }> = [];
    const prev = useStore.getState().openDialog;
    useStore.setState({
      openDialog: ((req: { title?: string; message: string; confirmLabel?: string; cancelLabel?: string; resolve: (v: unknown) => void }) => {
        opened.push(req);
        req.resolve(choice);
      }) as never,
    });
    return { opened, restore: () => useStore.setState({ openDialog: prev }) };
  }

  it('asks once with Connect / Not now, and connects on Connect', async () => {
    signedIn();
    const { bodies } = daemon([{ status: 'available' }, { status: 'connecting' }]);
    const dialog = answerDialog(true);
    await askToConnectAfterSignIn();
    dialog.restore();
    expect(dialog.opened).toHaveLength(1);
    expect(dialog.opened[0]).toMatchObject({
      title: CONNECT_PROMPT_TITLE,
      message: CONNECT_PROMPT_BODY,
      confirmLabel: 'Connect',
      cancelLabel: 'Not now',
    });
    expect(bodies).toEqual([{ force: false, check: true }, { force: true }]);
  });

  it('remembers Not now on the daemon', async () => {
    signedIn();
    const { bodies } = daemon([{ status: 'available' }, { status: 'declined' }]);
    const dialog = answerDialog(false);
    await askToConnectAfterSignIn();
    dialog.restore();
    expect(bodies).toEqual([{ force: false, check: true }, { force: false, decline: true }]);
  });

  it('does not ask after Not now, a Disconnect, or when already connected', async () => {
    for (const status of ['declined', 'connected']) {
      signedIn();
      const { bodies, spy } = daemon([{ status }]);
      const dialog = answerDialog(true);
      await askToConnectAfterSignIn();
      dialog.restore();
      expect(dialog.opened).toHaveLength(0);
      expect(bodies).toEqual([{ force: false, check: true }]);
      spy.mockRestore();
    }
  });
});

