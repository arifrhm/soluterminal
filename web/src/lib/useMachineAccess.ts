import { useSyncExternalStore } from 'react';
import { getMachineAccessState, subscribeMachineAccess } from './machineAccess';

export function useMachineAccess() {
  return useSyncExternalStore(subscribeMachineAccess, getMachineAccessState);
}
