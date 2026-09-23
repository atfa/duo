package coordinator

// Bootstrap policy lives in the Pi system prompt instead of forcing a
// coordinator-side wake-up. The human talks only to Austin through the Duo
// composer; Austin understands the task first, then uses duo_send to wake Tony
// and request an independent proposal. This preserves peer symmetry after
// startup and avoids inventing a second hidden copy of the user's prompt.
