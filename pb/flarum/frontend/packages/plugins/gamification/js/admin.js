// The Gamification extension's admin initialiser.
//
// The extension has no admin settings page of its own: the ranks are
// administered through the forum's own admin surface, and the settings are
// the forum's settings table. The bundle exists because a plugin declares
// both, and a missing one would be a 404 in the admin shell.
export const extend = [];
