import { useState } from 'react';
import { retryMachineAccess } from '../api';
import { useCloud } from '../cloud';
import { useMachineAccess } from '../lib/useMachineAccess';

export function MachineAccessDeniedState() {
  const { machineId } = useMachineAccess();
  const machines = useCloud((s) => s.machines);
  const [retrying, setRetrying] = useState(false);
  const machineName = machines.find((machine) => machine.id === machineId)?.name || machineId || 'this machine';

  const retry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await retryMachineAccess();
    } catch {
      // The denied state remains visible; a later manual retry can try again.
    } finally {
      setRetrying(false);
    }
  };

  return (
    <div className="bootstrap-error-state" role="alert" aria-live="assertive">
      <div className="bootstrap-error-card">
        <div className="bootstrap-error-kicker">No access</div>
        <h2>You don&apos;t have access to {machineName}</h2>
        <p>
          The cloud denied access to this machine. It may have been removed or
          re-paired, or your membership may have changed. Polling is paused.
        </p>
        <div className="bootstrap-error-actions">
          <button
            type="button"
            className="primary"
            onClick={() => useCloud.getState().deselectMachine()}
          >
            Switch machine
          </button>
          <button
            type="button"
            className="secondary"
            onClick={() => void retry()}
            disabled={retrying}
          >
            {retrying ? 'Retrying…' : 'Retry'}
          </button>
        </div>
      </div>
    </div>
  );
}
