// One answer to "is this a repository path the daemon will accept?", shared by
// every surface that asks for one (the adoption wizard's scope step, the
// Authority tab's grant editor).
//
// The daemon's rule is filepath.IsAbs on ITS platform (agents_store.go), and
// the browser cannot know whether that platform is Linux or Windows — so this
// accepts either form rather than guessing. It is a courtesy check, not the
// boundary: the daemon still refuses what it refuses. Its whole job is to say
// so before the user has walked five more screens.
export function isAbsoluteRepoPath(path: string): boolean {
  const p = path.trim();
  if (!p) return false;
  // POSIX, a Windows drive path, or a UNC share.
  return p.startsWith('/') || /^[a-zA-Z]:[\\/]/.test(p) || p.startsWith('\\\\');
}
