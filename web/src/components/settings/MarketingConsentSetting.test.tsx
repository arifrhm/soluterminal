import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { api } from '../../api';
import { useCloud } from '../../cloud';
import { MarketingConsentSetting } from './MarketingConsentSetting';

describe('MarketingConsentSetting', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    useCloud.setState({ session: null });
  });

  it('is hidden when signed out', () => {
    useCloud.setState({ session: null });
    const { container } = render(<MarketingConsentSetting />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the saved choice and changes it from Settings', async () => {
    useCloud.setState({ session: { access_token: 't', user: { id: 'u1' } } as never });
    vi.spyOn(api, 'getMarketingConsent').mockResolvedValue({ consent: false, updated_at: null });
    const put = vi.spyOn(api, 'putMarketingConsent').mockResolvedValue({ consent: true, updated_at: null });
    render(<MarketingConsentSetting />);
    const box = (await screen.findByLabelText(/email me product updates/i)) as HTMLInputElement;
    expect(box.checked).toBe(false);
    fireEvent.click(box);
    await waitFor(() => expect(put).toHaveBeenCalledWith(true, '2026-10-23-v1'));
    expect(box.checked).toBe(true);
  });

  it('hides itself when the cloud cannot be reached', async () => {
    useCloud.setState({ session: { access_token: 't', user: { id: 'u1' } } as never });
    vi.spyOn(api, 'getMarketingConsent').mockRejectedValue(new Error('offline'));
    const { container } = render(<MarketingConsentSetting />);
    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });
});
