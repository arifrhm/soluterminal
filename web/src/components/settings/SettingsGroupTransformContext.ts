import { createContext } from 'react';
import type { ReactNode } from 'react';

export type SettingsGroupTransform = {
  title: string;
  transform: (children: ReactNode) => ReactNode;
};

export const SettingsGroupTransformContext = createContext<SettingsGroupTransform | null>(null);
