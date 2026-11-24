// The Sticky extension's admin initialiser.
//
// The extension registers no admin page of its own: its permission is
// declared by the backend's permission table, which the admin's own
// Permissions page renders. The bundle exists because a plugin declares
// both, and a missing one would be a 404 in the admin shell.
export const extend = [];
