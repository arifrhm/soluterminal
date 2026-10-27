import { fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';
import { ApprovalActions } from './ApprovalActions';

describe('ApprovalActions', () => {
  afterEach(() => {
    useStore.setState({ senders: {} });
    vi.restoreAllMocks();
  });

  it('renders nothing until the pane has an input sender', () => {
    const { container } = render(<ApprovalActions paneId="p1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it('exposes all seven actions with descriptive accessible names', () => {
    useStore.setState({ senders: { p1: vi.fn() } });
    render(<ApprovalActions paneId="p1" />);

    const group = screen.getByRole('group', { name: 'Approval actions' });
    expect(within(group).getAllByRole('button')).toHaveLength(7);
    for (const name of [
      '1 — first choice',
      '2 — second choice',
      '3 — third choice',
      'y — yes',
      'n — no',
      'Enter — confirm default',
      'Escape — cancel',
    ]) {
      expect(within(group).getByRole('button', { name })).toBeInTheDocument();
    }
  });

  it('sends the exact bytes for yes and Enter', () => {
    const sender = vi.fn();
    useStore.setState({ senders: { p1: sender } });
    render(<ApprovalActions paneId="p1" />);

    fireEvent.click(screen.getByRole('button', { name: 'y — yes' }));
    fireEvent.click(screen.getByRole('button', { name: 'Enter — confirm default' }));
    expect(sender).toHaveBeenNthCalledWith(1, 'y');
    expect(sender).toHaveBeenNthCalledWith(2, '\r');
  });

  it('prevents mousedown default so pointer activation does not steal the terminal caret', () => {
    useStore.setState({ senders: { p1: vi.fn() } });
    render(<ApprovalActions paneId="p1" />);

    expect(fireEvent.mouseDown(screen.getByRole('button', { name: '1 — first choice' })))
      .toBe(false);
  });
  it('uses the captured scoped sender and refuses actions after its view changes',()=>{
    let active=true;const scoped=vi.fn(),coincident=vi.fn();
    useStore.setState({senders:{p1:coincident,'wsl-session':scoped}});
    render(<ApprovalActions paneId="p1" sessionKey="wsl-session" actionGuard={()=>active}/>);
    fireEvent.click(screen.getByRole('button',{name:'y — yes'}));expect(scoped).toHaveBeenCalledWith('y');expect(coincident).not.toHaveBeenCalled();
    active=false;fireEvent.click(screen.getByRole('button',{name:'1 — first choice'}));expect(scoped).toHaveBeenCalledTimes(1);
  });
});
